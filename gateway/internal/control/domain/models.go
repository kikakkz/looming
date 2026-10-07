// SPDX-License-Identifier: Apache-2.0

// Package domain holds the control plane's pure rules: revision
// backoff (backoff.go) and the model-permission expansion (models.go).
// Pure model — stdlib only (AD-23).
package domain

// ModelUseAll is the identity permission wildcard over the deployment's
// model catalog (identity slice D). The gateway — never identity —
// evaluates it: the catalog is engine-side configuration (AD-32), so
// expansion against it happens in this projection.
const ModelUseAll = "model:use:*"

// modelUsePrefix scopes a concrete model grant (model:use:<id>).
const modelUsePrefix = "model:use:"

// ExpandModels intersects a principal's effective permission set with
// the gateway's model catalog, yielding the models the subject may use
// here. model:use:* grants the whole catalog; model:use:<id> grants one
// model, kept only while the catalog still offers it (a grant for a
// model the engine no longer serves is inert, not an error). No
// model-use permission at all expands to nil — ModelAllowed denies on
// an empty list (front layer fails closed). The output is catalog
// order, so snapshot bytes stay deterministic.
func ExpandModels(permissions []string, catalog []string) []string {
	grantedAll := false
	concrete := map[string]bool{}
	for _, perm := range permissions {
		switch {
		case perm == ModelUseAll:
			grantedAll = true
		case len(perm) > len(modelUsePrefix) && perm[:len(modelUsePrefix)] == modelUsePrefix:
			concrete[perm[len(modelUsePrefix):]] = true
		}
	}
	var out []string
	for _, model := range catalog {
		if grantedAll || concrete[model] {
			out = append(out, model)
		}
	}
	return out
}
