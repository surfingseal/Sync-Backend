package model

type ImageInfo struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}

// UploadedImage contains request-scoped bytes ready for a future AI client.
// Data must never be serialized in an API response.
type UploadedImage struct {
	ImageInfo
	Data []byte `json:"-"`
}
