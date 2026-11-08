package dsm

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ErrNotFound = errors.New("DSM object not found")

type APIError struct {
	API    string
	Method string
	Code   int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("DSM rejected %s.%s (code %d)", e.API, e.Method, e.Code)
}

type Capability struct {
	Path string `json:"path"`
	Min  int    `json:"minVersion"`
	Max  int    `json:"maxVersion"`
}

type Client struct {
	endpoint string
	http     *http.Client
	username string
	password string
	otp      string
	sid      string
	token    string
	apis     map[string]Capability
	mu       sync.Mutex
	mutation sync.Mutex
}

func (*Client) String() string   { return "DSM client (credentials redacted)" }
func (*Client) GoString() string { return "DSM client (credentials redacted)" }

func (c *Client) IsSessionUser(name string) bool { return name == c.username }

func New(endpoint, username, password, otp, caFile string) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("endpoint must be an HTTPS origin without credentials, path, query, or fragment")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, errors.New("cannot read ca_file")
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, errors.New("cannot load system certificate authorities")
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("ca_file contains no valid PEM certificates")
		}
		transport.TLSClientConfig.RootCAs = roots
	}
	return &Client{
		endpoint: strings.TrimSuffix(u.String(), "/"), username: username, password: password, otp: otp,
		http: &http.Client{Transport: transport, Timeout: 90 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var info map[string]Capability
	if err := c.request(ctx, "query.cgi", "SYNO.API.Info", 1, "query", url.Values{"query": {"all"}}, &info, false); err != nil {
		return err
	}
	if len(info) == 0 {
		return errors.New("DSM returned an empty API capability catalog")
	}
	c.apis = info
	cap, ok := info["SYNO.API.Auth"]
	if !ok || cap.Min > 6 || cap.Max < 6 {
		return errors.New("DSM does not advertise the required authentication API version 6")
	}
	var auth struct {
		SID   string `json:"sid"`
		Token string `json:"synotoken"`
	}
	args := url.Values{"account": {c.username}, "passwd": {c.password}, "session": {"Terraform"}, "format": {"sid"}, "enable_syno_token": {"yes"}}
	if c.otp != "" {
		args.Set("otp_code", c.otp)
	}
	if err := c.request(ctx, cap.Path, "SYNO.API.Auth", 6, "login", args, &auth, false); err != nil {
		return err
	}
	if auth.SID == "" {
		return errors.New("DSM authentication returned no session identifier")
	}
	c.sid, c.token = auth.SID, auth.Token
	return nil
}

func (c *Client) Call(ctx context.Context, api string, version int, method string, args url.Values, result any) error {
	cap, ok := c.apis[api]
	if !ok || version < cap.Min || version > cap.Max {
		return fmt.Errorf("DSM does not advertise required %s version %d", api, version)
	}
	return c.request(ctx, cap.Path, api, version, method, args, result, true)
}

func (c *Client) Close(ctx context.Context) error {
	return c.Call(ctx, "SYNO.API.Auth", 6, "logout", url.Values{"session": {"Terraform"}}, nil)
}

func (c *Client) request(ctx context.Context, path, api string, version int, method string, args url.Values, result any, authenticated bool) error {
	if path == "" || strings.ContainsAny(path, "/\\?#:") || !strings.HasSuffix(path, ".cgi") {
		return errors.New("DSM advertised an unsafe API path")
	}
	form := url.Values{"api": {api}, "version": {strconv.Itoa(version)}, "method": {method}}
	for k, v := range args {
		form[k] = append([]string(nil), v...)
	}
	if authenticated {
		form.Set("_sid", c.sid)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/webapi/"+path, strings.NewReader(form.Encode()))
	if err != nil {
		return errors.New("cannot construct DSM request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if authenticated && c.token != "" {
		req.Header.Set("X-SYNO-TOKEN", c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("DSM %s.%s transport failed; verify connectivity and the endpoint certificate; outcome may be uncertain after a write", api, method)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("DSM %s.%s returned HTTP %d", api, method, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024+1))
	if err != nil || len(body) > 8*1024*1024 {
		return errors.New("DSM response is incomplete or exceeds the size limit")
	}
	var envelope struct {
		Success *bool           `json:"success"`
		Data    json.RawMessage `json:"data"`
		Finish  json.RawMessage `json:"finish"`
		Error   struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Success == nil {
		return fmt.Errorf("DSM %s.%s returned a malformed envelope", api, method)
	}
	if !*envelope.Success {
		return &APIError{API: api, Method: method, Code: envelope.Error.Code}
	}
	if reader, ok := result.(interface {
		decodeEnvelope(json.RawMessage, json.RawMessage) error
	}); ok {
		if err := reader.decodeEnvelope(envelope.Data, envelope.Finish); err != nil {
			return fmt.Errorf("DSM %s.%s returned malformed task data", api, method)
		}
		return nil
	}
	if result != nil {
		if len(envelope.Data) == 0 || string(envelope.Data) == "null" || json.Unmarshal(envelope.Data, result) != nil {
			return fmt.Errorf("DSM %s.%s returned malformed data", api, method)
		}
	}
	return nil
}

func (c *Client) Mutate(ctx context.Context, api string, version int, method string, args url.Values) error {
	return c.MutateAfterRead(ctx, api, version, method, func() (url.Values, error) { return args, nil })
}

func (c *Client) MutateAfterRead(ctx context.Context, api string, version int, method string, prepare func() (url.Values, error)) error {
	var reply MutationReply
	if err := c.MutateAfterReadResult(ctx, api, version, method, prepare, &reply); err != nil {
		return err
	}
	if reply.Failed() || reply.TaskID != "" || (reply.Finish != nil && !*reply.Finish) {
		return fmt.Errorf("DSM %s.%s did not acknowledge a successful synchronous mutation; inspect and reconcile before retrying", api, method)
	}
	return nil
}

func (c *Client) MutateAfterReadResult(ctx context.Context, api string, version int, method string, prepare func() (url.Values, error), result any) error {
	return c.WithMutation(func() error {
		args, err := prepare()
		if err != nil || args == nil {
			return err
		}
		return c.Call(ctx, api, version, method, args, result)
	})
}

func JSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (c *Client) WithMutation(operation func() error) error {
	c.mutation.Lock()
	defer c.mutation.Unlock()
	return operation()
}

func (c *Client) UsesHTTPSPort(port int) bool {
	endpoint, err := url.Parse(c.endpoint)
	if err != nil {
		return false
	}
	actual := 443
	if endpoint.Port() != "" {
		actual, err = strconv.Atoi(endpoint.Port())
		if err != nil {
			return false
		}
	}
	return actual == port
}
