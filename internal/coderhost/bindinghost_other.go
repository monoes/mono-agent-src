//go:build !linux

package coderhost

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/monoes/mono-agent/internal/procbind"
)

// BindingHost is the binding service's owner; the service needs Linux, so here it never runs.
type BindingHost struct {
	DB     *sql.DB
	Binder *procbind.Binder
	Root   string
	Logf   func(format string, args ...any)
	Now    func() time.Time
}

func (h *BindingHost) Running(string) bool { return false }
func (h *BindingHost) Ensure(context.Context, string) error {
	return errors.New("the binding service needs Linux")
}
func (h *BindingHost) Shutdown() {}

// BindingStatus is the state of one profile's service; here there is none.
type BindingStatus struct {
	Profile string `json:"profile"`
	Running bool   `json:"running"`
	Start   int64  `json:"start"`
	Pending int    `json:"pending"`
	Active  int    `json:"active"`
	Revoked int    `json:"revoked"`
}

func (h *BindingHost) Status() []BindingStatus { return nil }
