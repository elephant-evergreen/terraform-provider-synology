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

	"golang.org/x/sync/singleflight"
)

var ErrNotFound = errors.New("DSM object not found")

type APIError struct {
	API    string
	Method string
	Code   int
}

type UnsupportedAPIError struct {
	API     string
	Version int
}

func (e *UnsupportedAPIError) Error() string {
	return fmt.Sprintf("DSM does not advertise required %s version %d", e.API, e.Version)
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
	endpoint  string
	http      *http.Client
	username  string
	password  string
	otp       string
	sid       string
	token     string
	apis      map[string]Capability
	mu        sync.Mutex
	mutation  sync.RWMutex
	packages  singleflight.Group
	serialMu  sync.Mutex
	serial    string
	sessionMu sync.RWMutex
	auth      singleflight.Group
}

func (*Client) String() string   { return "DSM client (credentials redacted)" }
func (*Client) GoString() string { return "DSM client (credentials redacted)" }

func (c *Client) IsSessionUser(name string) bool { return name == c.username }

func (c *Client) Serial(ctx context.Context) (string, error) {
	c.serialMu.Lock()
	defer c.serialMu.Unlock()
	if c.serial != "" {
		return c.serial, nil
	}
	var info struct {
		Serial string `json:"serial"`
	}
	if err := c.Call(ctx, "SYNO.Core.System", 1, "info", nil, &info); err != nil {
		return "", err
	}
	if info.Serial == "" {
		return "", errors.New("DSM returned no device serial")
	}
	c.serial = info.Serial
	return c.serial, nil
}

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
	return c.login(ctx)
}

func (c *Client) login(ctx context.Context) error {
	cap, ok := c.apis["SYNO.API.Auth"]
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
		var apiErr *APIError
		if errors.As(err, &apiErr) && (apiErr.Code == 499 || apiErr.Code == 407) {
			return fmt.Errorf("DSM rejected the login (%d): the source IP may be blocked or the account may have reached its session limit; unblock the IP or sign out stale sessions in DSM, then retry", apiErr.Code)
		}
		return err
	}
	if auth.SID == "" {
		return errors.New("DSM authentication returned no session identifier")
	}
	c.sessionMu.Lock()
	c.sid, c.token = auth.SID, auth.Token
	c.sessionMu.Unlock()
	return nil
}

func (c *Client) reauthenticate(ctx context.Context) error {
	_, err, _ := c.auth.Do("login", func() (any, error) {
		return nil, c.login(ctx)
	})
	return err
}

func readMethod(method string) bool {
	switch method {
	case "get", "list", "status", "info", "query":
		return true
	}
	return false
}

func sessionExpired(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.Code == 106 || apiErr.Code == 107 || apiErr.Code == 119
}

func (c *Client) Call(ctx context.Context, api string, version int, method string, args url.Values, result any) error {
	cap, ok := c.apis[api]
	if !ok || version < cap.Min || version > cap.Max {
		return &UnsupportedAPIError{API: api, Version: version}
	}
	err := c.request(ctx, cap.Path, api, version, method, args, result, true)
	if err == nil || !readMethod(method) || !sessionExpired(err) {
		return err
	}
	if reauthErr := c.reauthenticate(ctx); reauthErr != nil {
		return err
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
	sid, token := "", ""
	if authenticated {
		c.sessionMu.RLock()
		sid, token = c.sid, c.token
		c.sessionMu.RUnlock()
		form.Set("_sid", sid)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/webapi/"+path, strings.NewReader(form.Encode()))
	if err != nil {
		return errors.New("cannot construct DSM request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if token != "" {
		req.Header.Set("X-SYNO-TOKEN", token)
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
