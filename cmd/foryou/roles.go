package main

import (
	"context"
	"net/http"
	"slices"
)

// service is one role's handler and background work; Run returns nil at once without loops.
type service interface {
	Handler() http.Handler
	Run(ctx context.Context) error
	RunOnce(ctx context.Context) error
	Close() error
}

// builder constructs a service from its role configuration.
type builder func(roleConfig) (service, error)

// builders maps each role to its current service builder.
var builders = demoBuilders()

// registerRole replaces the builder for a role in roleOrder.
func registerRole(name string, b builder) {
	if !slices.Contains(roleOrder, name) {
		panic("unknown role: " + name)
	}
	builders[name] = b
}

// commands maps whole-process command names to their runners.
var commands = map[string]func(ctx context.Context, env func(string) string) error{}
