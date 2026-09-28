package tool

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
)

// LoadYAML reads the YAML file at path into out (a pointer to a struct)
// using Viper, decoding by each field's existing `yaml:"..."` tag rather
// than requiring a parallel set of `mapstructure:"..."` tags.
//
// Before handing the file to Viper, it's scanned for an unquoted scalar
// that looks like a leading-zero number (e.g. "007954000891379") --
// confirmed live, Viper's underlying YAML decode parses that as a number
// and silently drops the leading zero(s) well before Unmarshal ever sees
// it, regardless of the destination Go field being a string. A playbook
// field like a device serial is exactly this shape, so this check runs
// unconditionally rather than being opt-in.
func LoadYAML(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading playbook: %w", err)
	}
	if err := checkNoUnquotedLeadingZeros(data); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	v := viper.New()
	v.SetConfigType("yaml")
	if err := v.ReadConfig(bytes.NewReader(data)); err != nil {
		return fmt.Errorf("reading playbook: %w", err)
	}
	if err := v.Unmarshal(out, func(c *mapstructure.DecoderConfig) {
		c.TagName = "yaml"
	}); err != nil {
		return fmt.Errorf("parsing playbook: %w", err)
	}
	return nil
}

// leadingZeroValue matches a line whose YAML value is an unquoted scalar
// consisting of a leading zero followed by at least one more digit (e.g.
// "007954000891379", or "-0123") -- optionally preceded by a block
// sequence marker ("- "), a "key:" (or both, e.g. "- serial: 0123"), and
// optionally followed by a trailing "# comment".
//
// Deliberately a single-line regex rather than a real YAML parse: it's a
// best-effort guard against the one shape that actually bites (see
// LoadYAML's doc comment), not a general linter -- flow-style collections
// (`[007, 008]`, `{serial: 007}`) and multi-line scalars aren't handled,
// but every playbook in this repo (and every one this tool expects to
// load) is written in plain block style, one key or sequence item per
// line, which this fully covers.
var leadingZeroValue = regexp.MustCompile(`^\s*(?:-\s+)?(?:[\w.$-]+\s*:\s*)?(-?0[0-9]+)\s*(?:#.*)?$`)

// checkNoUnquotedLeadingZeros scans raw YAML line by line for a value
// leadingZeroValue matches, returning an error identifying the first one
// found. A quoted value ("007954000891379" or '007954000891379') never
// matches -- the quote characters break the pattern -- so this never
// flags a value someone already protected correctly.
func checkNoUnquotedLeadingZeros(raw []byte) error {
	for i, line := range strings.Split(string(raw), "\n") {
		m := leadingZeroValue.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		return fmt.Errorf("line %d: unquoted value %s looks numeric with a leading zero and will silently lose it when parsed -- quote it (%q) instead: %s",
			i+1, m[1], m[1], strings.TrimSpace(line))
	}
	return nil
}
