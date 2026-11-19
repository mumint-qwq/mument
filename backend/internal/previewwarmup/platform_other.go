//go:build !linux || 386 || arm

package previewwarmup

import (
	"context"
	"os"
)

const platformSupported = false

type scopeID struct{}
type guardedFile struct {
	scope           *scope
	relative        string
	version         string
	claimedIdentity scopeID
}

func (*scope) online() (scopeID, error)                       { return scopeID{}, errScopeOffline }
func (guardedFile) open(context.Context) (*os.File, error)    { return nil, errScopeOffline }
func (guardedFile) stat(context.Context) (os.FileInfo, error) { return nil, errScopeOffline }

type resourceProbe struct{}

func newResourceProbe(string) resourceProbe { return resourceProbe{} }
func (*resourceProbe) poll() error          { return errPressureUnavailable }
func (*resourceProbe) check(int64) error    { return errPressureUnavailable }
