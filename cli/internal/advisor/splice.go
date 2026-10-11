// SPDX-License-Identifier: Apache-2.0

package advisor

import (
	"bytes"
	"fmt"
	"reflect"

	"gopkg.in/yaml.v3"

	"github.com/kikakkz/looming/platform/go/config"
)

// placementOut is the rendered shape of one entry in the topology
// file's placements: section — field order fixed, optional fields
// omitted, matching the operator-facing fixtures' flow style.
type placementOut struct {
	Component  string            `yaml:"component"`
	Host       string            `yaml:"host"`
	Ports      map[string]int    `yaml:"ports,omitempty"`
	Config     map[string]string `yaml:"config,omitempty"`
	ExtraHosts []string          `yaml:"extra_hosts,omitempty"`
	EnvFile    string            `yaml:"env_file,omitempty"`
}

// existingPlacement is one entry of the document's current placements
// section: the typed value (from config.Load, which preserves document
// order) plus its original node, for reuse when the entry is untouched.
type existingPlacement struct {
	typed config.Placement
	node  *yaml.Node
}

// splicePlacements replaces — or, when the file has none, appends — the
// top-level placements: section with the new list. Entries unchanged
// from the previous list reuse their original document nodes, so the
// operator's formatting (flow styles, comments) survives and the diff
// shows exactly what the proposal changed; new or touched entries are
// rendered through the tagged output shape.
func splicePlacements(document []byte, placements []config.Placement, previous []config.Placement) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(document, &doc); err != nil {
		return nil, fmt.Errorf("advise: parse topology document: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("advise: topology document is not a mapping")
	}
	root := doc.Content[0]

	seq, err := buildPlacementsSequence(placements, existingByComponent(root, previous))
	if err != nil {
		return nil, err
	}

	replaced := false
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "placements" {
			root.Content[i+1] = seq
			replaced = true
		}
	}
	if !replaced {
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "placements"}, seq)
	}

	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return nil, fmt.Errorf("advise: encode topology document: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("advise: encode topology document: %w", err)
	}
	return out.Bytes(), nil
}

// existingByComponent pairs the document's placement entry nodes with
// the previous typed list (config.Load preserves document order) and
// indexes them by component.
func existingByComponent(root *yaml.Node, previous []config.Placement) map[string]existingPlacement {
	out := map[string]existingPlacement{}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "placements" || root.Content[i+1].Kind != yaml.SequenceNode {
			continue
		}
		items := root.Content[i+1].Content
		for j, node := range items {
			if j >= len(previous) {
				break
			}
			out[previous[j].Component] = existingPlacement{typed: previous[j], node: node}
		}
	}
	return out
}

// buildPlacementsSequence assembles the new section in the new list's
// order: an entry identical to its previous self reuses the original
// node; anything new or touched renders through the tagged shape.
func buildPlacementsSequence(placements []config.Placement, existing map[string]existingPlacement) (*yaml.Node, error) {
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	for _, p := range placements {
		if prev, ok := existing[p.Component]; ok && reflect.DeepEqual(prev.typed, p) {
			seq.Content = append(seq.Content, prev.node)
			continue
		}
		node, err := renderPlacementNode(p)
		if err != nil {
			return nil, err
		}
		seq.Content = append(seq.Content, node)
	}
	return seq, nil
}

// renderPlacementNode encodes one placement through the tagged output
// shape and parses it back into a node — the encoder owns the YAML
// syntax, the splice owns the tree position.
func renderPlacementNode(p config.Placement) (*yaml.Node, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(placementOut{
		Component:  p.Component,
		Host:       p.Host,
		Ports:      p.Ports,
		Config:     p.Config,
		ExtraHosts: p.ExtraHosts,
		EnvFile:    p.EnvFile,
	}); err != nil {
		return nil, fmt.Errorf("advise: encode placements: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("advise: encode placements: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(buf.Bytes(), &doc); err != nil {
		return nil, fmt.Errorf("advise: parse rendered placements: %w", err)
	}
	if len(doc.Content) == 0 {
		return nil, fmt.Errorf("advise: rendered placements are empty")
	}
	return doc.Content[0], nil
}
