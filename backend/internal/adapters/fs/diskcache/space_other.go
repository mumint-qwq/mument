//go:build !linux

package diskcache

import "context"

type Inventory struct{}

func (*ReclaimTree) SpaceSample() (SpaceSample, error)           { return SpaceSample{}, ErrReclaimScope }
func (*ReclaimTree) NewInventory() (*Inventory, error)           { return nil, ErrReclaimScope }
func (*ReclaimTree) Store(context.Context, string, []byte) error { return ErrReclaimScope }
func (*Inventory) Step(context.Context, int) (Usage, bool, error) {
	return Usage{}, false, ErrReclaimScope
}
func (*Inventory) Close()           {}
func SpaceWriteReason(error) string { return "" }
