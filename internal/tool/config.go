package tool

import (
	"fmt"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
)

// LoadYAML reads the YAML file at path into out (a pointer to a struct)
// using Viper, decoding by each field's existing `yaml:"..."` tag rather
// than requiring a parallel set of `mapstructure:"..."` tags.
func LoadYAML(path string, out any) error {
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")

	if err := v.ReadInConfig(); err != nil {
		return fmt.Errorf("reading playbook: %w", err)
	}
	if err := v.Unmarshal(out, func(c *mapstructure.DecoderConfig) {
		c.TagName = "yaml"
	}); err != nil {
		return fmt.Errorf("parsing playbook: %w", err)
	}
	return nil
}
