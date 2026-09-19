// Package config adapts the public configuration model to the binary's
// compiled capability registry.
package config

import (
	"errors"
	"io"

	"github.com/diasYuri/y/internal/feature"
	publicconfig "github.com/diasYuri/y/pkg/config"
)

type Config = publicconfig.Config
type Error = publicconfig.Error

func LoadFile(path string) (Config, error) {
	return publicconfig.LoadFile(path)
}

func LoadFileWithLookup(path string, lookup func(string) string) (Config, error) {
	return publicconfig.LoadFileWithLookup(path, lookup)
}

func Parse(r io.Reader) (Config, error) {
	return publicconfig.Parse(r)
}

func ApplyEnvironment(cfg Config, lookup func(string) string) Config {
	return publicconfig.ApplyEnvironment(cfg, lookup)
}

func Validate(cfg Config, compiled *feature.Registry) error {
	if compiled == nil {
		return errors.New("compiled feature registry is nil")
	}
	return publicconfig.Validate(cfg, compiledRegistry{registry: compiled})
}

func GenerateDefault() string {
	return publicconfig.GenerateDefault()
}

type compiledRegistry struct {
	registry *feature.Registry
}

func (r compiledRegistry) IsKnown(kind publicconfig.CapabilityKind, id string) bool {
	return feature.IsKnown(feature.Kind(kind), id)
}

func (r compiledRegistry) IsCompiled(kind publicconfig.CapabilityKind, id string) bool {
	return r.registry.Has(feature.Kind(kind), id)
}
