// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"reflect"
	"testing"
)

func TestExpandModels(t *testing.T) {
	catalog := []string{"gpt-5", "claude-sonnet", "deepseek-chat"}
	cases := []struct {
		name        string
		permissions []string
		want        []string
	}{
		{
			name:        "wildcard grants the whole catalog in catalog order",
			permissions: []string{"model:use:*"},
			want:        catalog,
		},
		{
			name:        "concrete grant kept while catalog offers it",
			permissions: []string{"model:use:gpt-5"},
			want:        []string{"gpt-5"},
		},
		{
			name:        "concrete grant outside the catalog is inert",
			permissions: []string{"model:use:llama-90b"},
			want:        nil,
		},
		{
			name:        "wildcard and concrete union without duplicates",
			permissions: []string{"model:use:*", "model:use:gpt-5"},
			want:        catalog,
		},
		{
			name:        "unrelated permissions expand to nothing",
			permissions: []string{"gateway:use", "quota:view"},
			want:        nil,
		},
		{
			name:        "empty permissions deny everything",
			permissions: nil,
			want:        nil,
		},
		{
			name:        "prefix-shaped but empty model id is ignored",
			permissions: []string{"model:use:"},
			want:        nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExpandModels(tc.permissions, catalog); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestExpandModelsEmptyCatalog(t *testing.T) {
	if got := ExpandModels([]string{"model:use:*"}, nil); got != nil {
		t.Fatalf("empty catalog must expand to nothing, got %v", got)
	}
}
