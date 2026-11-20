//go:build !linux

package diskcache

import (
	"context"
	"os"
)

type ReclaimTree struct{}
type CacheObject struct{}

func OpenReclaimTree(string) (*ReclaimTree, error)                     { return nil, ErrReclaimScope }
func (*ReclaimTree) Close() error                                      { return nil }
func (*ReclaimTree) HasDurableRoot() bool                              { return false }
func (*ReclaimTree) CheckRecordedRoot(DirectoryIdentity) error         { return ErrReclaimScope }
func (*ReclaimTree) Check(bool) error                                  { return ErrReclaimScope }
func (*ReclaimTree) OpenKey(string) (*CacheObject, error)              { return nil, ErrReclaimScope }
func (*ReclaimTree) OpenIsolated(string, bool) (*CacheObject, error)   { return nil, ErrReclaimScope }
func (*CacheObject) Close() error                                      { return nil }
func (*CacheObject) Check(ObjectStamp) error                           { return ErrReclaimScope }
func (*CacheObject) Stamp() (ObjectStamp, error)                       { return ObjectStamp{}, ErrReclaimScope }
func (*CacheObject) Verify(context.Context, ObjectStamp, string) error { return ErrReclaimScope }
func (*CacheObject) Move(*CacheObject, ObjectStamp) error              { return ErrReclaimScope }
func (*CacheObject) Unlink(ObjectStamp) error                          { return ErrReclaimScope }
func (SourceProof) Check() error                                       { return ErrReclaimScope }
func ObserveSource(string, string) (*SourceObservation, error)         { return nil, ErrReclaimScope }
func (*SourceObservation) Unlinked(bool) bool                          { return false }
func (*SourceObservation) Updated(os.FileInfo) bool                    { return false }
func ReclaimReason(error) string                                       { return "unsupported_platform" }

func ObserveSourceDirectory(context.Context, string, string, int) (*SourceDirectory, error) {
	return nil, ErrReclaimScope
}
func (*SourceDirectory) Contains(string, os.FileInfo) bool { return false }
func (*SourceDirectory) Removed() bool                     { return false }
func (*SourceObservation) LostLink() bool                  { return false }
