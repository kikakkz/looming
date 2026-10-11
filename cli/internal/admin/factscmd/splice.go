// SPDX-License-Identifier: Apache-2.0

package factscmd

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/kikakkz/looming/platform/go/config"
)

// capabilitiesOut is the rendered shape of one hosts[].capabilities
// block: zero facts and undeclared zone are omitted, so a partial
// observation renders a partial block rather than a wall of zeros.
type capabilitiesOut struct {
	Hardware *hardwareOut `yaml:"hardware,omitempty"`
	Network  *networkOut  `yaml:"network,omitempty"`
}

type hardwareOut struct {
	CPUCores int    `yaml:"cpu_cores,omitempty"`
	MemoryMB int    `yaml:"memory_mb,omitempty"`
	DiskGB   int    `yaml:"disk_gb,omitempty"`
	Arch     string `yaml:"arch,omitempty"`
}

type networkOut struct {
	Zone        string         `yaml:"zone,omitempty"`
	Egress      *bool          `yaml:"egress,omitempty"`
	LatenciesMS map[string]int `yaml:"latencies_ms,omitempty"`
}

// renderCapabilities maps the merged block onto the rendered shape.
func renderCapabilities(caps *config.Capabilities) capabilitiesOut {
	out := capabilitiesOut{}
	hw := caps.Hardware
	if hw.CPUCores > 0 || hw.MemoryMB > 0 || hw.DiskGB > 0 || hw.Arch != "" {
		out.Hardware = &hardwareOut{
			CPUCores: hw.CPUCores,
			MemoryMB: hw.MemoryMB,
			DiskGB:   hw.DiskGB,
			Arch:     hw.Arch,
		}
	}
	nw := caps.Network
	if nw.Zone != "" || nw.Egress != nil || len(nw.LatenciesMS) > 0 {
		out.Network = &networkOut{
			Zone:        nw.Zone,
			Egress:      nw.Egress,
			LatenciesMS: nw.LatenciesMS,
		}
	}
	return out
}

// capabilitiesNode encodes the merged block through the tagged shape
// and parses it back into a document node — the encoder owns the YAML
// syntax, the splice owns the tree position (the same division
// advise's placements splice uses).
func capabilitiesNode(caps *config.Capabilities) (*yaml.Node, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(renderCapabilities(caps)); err != nil {
		return nil, fmt.Errorf("facts: encode capabilities: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("facts: encode capabilities: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(buf.Bytes(), &doc); err != nil {
		return nil, fmt.Errorf("facts: parse rendered capabilities: %w", err)
	}
	if len(doc.Content) == 0 {
		return nil, fmt.Errorf("facts: rendered capabilities are empty")
	}
	return doc.Content[0], nil
}

// spliceCapabilities rewrites the capabilities block of every host in
// the merged set inside the raw document. Callers pass only hosts the
// merge actually changed, so every merged entry gets a freshly
// rendered block; everything outside hosts[].capabilities — untouched
// host entries, placements, comments — survives by the node splice.
func spliceCapabilities(document []byte, hosts []config.Host, merged map[string]*config.Capabilities) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(document, &doc); err != nil {
		return nil, fmt.Errorf("facts: parse topology document: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("facts: topology document is not a mapping")
	}
	root := doc.Content[0]

	touched := 0
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "hosts" || root.Content[i+1].Kind != yaml.SequenceNode {
			continue
		}
		for j, node := range root.Content[i+1].Content {
			if j >= len(hosts) || node.Kind != yaml.MappingNode {
				continue
			}
			caps, ok := merged[hosts[j].ID]
			if !ok {
				continue
			}
			if err := setCapabilitiesNode(node, caps); err != nil {
				return nil, err
			}
			touched++
		}
	}
	if touched != len(merged) {
		return nil, fmt.Errorf("facts: %d merged host(s) not found in the document's hosts section", len(merged)-touched)
	}

	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return nil, fmt.Errorf("facts: encode topology document: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("facts: encode topology document: %w", err)
	}
	return out.Bytes(), nil
}

// setCapabilitiesNode swaps one host entry's capabilities value in
// place; a host with no capabilities key yet gains one at the end of
// its mapping.
func setCapabilitiesNode(entry *yaml.Node, caps *config.Capabilities) error {
	node, err := capabilitiesNode(caps)
	if err != nil {
		return err
	}
	for k := 0; k+1 < len(entry.Content); k += 2 {
		if entry.Content[k].Value == "capabilities" {
			entry.Content[k+1] = node
			return nil
		}
	}
	entry.Content = append(entry.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "capabilities"}, node)
	return nil
}
