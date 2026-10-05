package model

// AlbumArtwork is provider metadata, never a YouTube thumbnail or downloaded image.
type AlbumArtwork struct {
	AlbumTitle string `json:"album_title"`
	URL        string `json:"album_artwork_url"`
	Source     string `json:"source"`
}
