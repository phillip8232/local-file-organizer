package platform

// Volume is a fixed drive or mounted filesystem.
type Volume struct {
	Root   string `json:"root"`  // C:\ or /mnt/data
	Label  string `json:"label"` // volume name, may be empty
	FS     string `json:"fs"`
	Total  int64  `json:"total"`
	Free   int64  `json:"free"`
	System bool   `json:"system"` // holds the OS
}

// Used is the number of bytes in use.
func (v Volume) Used() int64 { return v.Total - v.Free }
