package diskcache

import "errors"

var ErrSpaceChanged = errors.New("inventory_changed")

type SpaceSample struct {
	Available int64             `json:"available_bytes"`
	Block     int64             `json:"allocation_unit_bytes"`
	Inodes    uint64            `json:"free_inodes"`
	HasInodes bool              `json:"inode_count_available"`
	ReadOnly  bool              `json:"read_only"`
	Mount     uint64            `json:"mount_id"`
	Root      DirectoryIdentity `json:"root_identity"`
}

type Usage struct {
	Logical   int64 `json:"logical_bytes"`
	Allocated int64 `json:"allocated_bytes"`
	Files     int64 `json:"unique_files"`
	Excluded  int64 `json:"excluded_entries"`
}
