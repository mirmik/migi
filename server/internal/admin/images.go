package admin

import (
	"path"
	"strings"
)

// Only raster formats may be served inline on the administration origin.
// An extension is a fallback for uploads without a specific media type.
func sharedImageMIME(file SharedFile) string {
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(file.MIME, ";", 2)[0]))
	switch mediaType {
	case "image/jpeg", "image/png", "image/gif", "image/webp", "image/bmp", "image/avif":
		return mediaType
	case "image/jpg":
		return "image/jpeg"
	case "image/x-ms-bmp":
		return "image/bmp"
	case "", "application/octet-stream":
		switch strings.ToLower(path.Ext(file.Name)) {
		case ".jpg", ".jpeg":
			return "image/jpeg"
		case ".png":
			return "image/png"
		case ".gif":
			return "image/gif"
		case ".webp":
			return "image/webp"
		case ".bmp":
			return "image/bmp"
		case ".avif":
			return "image/avif"
		}
	}
	return ""
}
