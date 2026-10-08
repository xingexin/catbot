package health

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

func Runtime(ctx context.Context, url, token string) (any, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url+"/health", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("runtime health: HTTP %d", resp.StatusCode)
	}
	var value any
	err = json.NewDecoder(resp.Body).Decode(&value)
	return value, err
}
