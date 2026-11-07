package provider

import (
	"context"
	"errors"

	"github.com/elephant-evergreen/terraform-provider-synology/internal/dsm"
	"github.com/hashicorp/terraform-plugin-framework/diag"
)

type privateReader interface {
	GetKey(context.Context, string) ([]byte, diag.Diagnostics)
}

func guardUncertainCreation(ctx context.Context, private privateReader) diag.Diagnostics {
	value, diags := private.GetKey(ctx, "creation_outcome")
	if diags.HasError() {
		return diags
	}
	if len(value) != 0 {
		diags.AddError("Creation outcome needs explicit reconciliation", "DSM did not confirm a stable identity for this creation. The provider will not adopt or delete a same-name object automatically. Back up state, inspect DSM, then remove only this provisional state entry and explicitly import the confirmed object. Do not recreate or destroy data to resolve this error.")
	}
	return diags
}

func rejectedCreation(err error) bool { var rejection *dsm.APIError; return errors.As(err, &rejection) }
