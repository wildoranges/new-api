package jsrt

import (
	"encoding/json"
	"net/url"
	"strings"
)


func parseBodyByType(bodyBytes []byte, contentType string) any {
	if len(bodyBytes) == 0 {
		return ""
	}

	bodyStr := string(bodyBytes)
	contentLower := strings.ToLower(contentType)

	switch {
	case strings.Contains(contentLower, "application/json"):
		var jsonObj any
		if err := json.Unmarshal(bodyBytes, &jsonObj); err == nil {
			return jsonObj
		}
		return bodyStr

	case strings.Contains(contentLower, "application/x-www-form-urlencoded"):
		if values, err := url.ParseQuery(bodyStr); err == nil {
			result := make(map[string]string, len(values))
			for k, v := range values {
				if len(v) > 0 {
					result[k] = v[0]
				}
			}
			return result
		}
		return bodyStr

	case strings.Contains(contentLower, "multipart/form-data"):
		return bodyBytes

	case strings.Contains(contentLower, "text/"):
		return bodyStr

	default:
		// 尝试JSON解析
		var jsonObj any
		if json.Unmarshal(bodyBytes, &jsonObj) == nil {
			return jsonObj
		}

		// 尝试form解析
		if values, err := url.ParseQuery(bodyStr); err == nil && len(values) > 0 {
			result := make(map[string]string, len(values))
			for k, v := range values {
				if len(v) > 0 {
					result[k] = v[0]
				}
			}
			return result
		}

		return bodyStr
	}
}