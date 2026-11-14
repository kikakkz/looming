// SPDX-License-Identifier: Apache-2.0

package fabric

import (
	"errors"
	"fmt"
	"regexp"
)

var (
	ErrMissingField    = errors.New("fabric: required node config field is missing")
	ErrInvalidHostname = errors.New("fabric: hostname must be a lowercase DNS label")
)

var hostnamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

type NodeConfig struct {
	ControlURL string
	AuthKey    string
	StateDir   string
	Hostname   string
}

func (c NodeConfig) Validate() error {
	fields := []struct {
		name  string
		value string
	}{
		{"ControlURL", c.ControlURL},
		{"AuthKey", c.AuthKey},
		{"StateDir", c.StateDir},
		{"Hostname", c.Hostname},
	}
	for _, f := range fields {
		if f.value == "" {
			return fmt.Errorf("%w: %s", ErrMissingField, f.name)
		}
	}
	if !hostnamePattern.MatchString(c.Hostname) {
		return fmt.Errorf("%w: %q", ErrInvalidHostname, c.Hostname)
	}
	return nil
}
