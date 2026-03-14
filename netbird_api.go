package kea_traefik_plugin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type peerGroup struct {
	Name string `json:"name"`
}

type peer struct {
	IP     string      `json:"ip"`
	Groups []peerGroup `json:"groups"`
}

// GroupPeers maps each group name to the list of peer IPs that belong to it.
type GroupPeers map[string][]string

// FetchNetbirdPeerGroups calls the NetBird API peers endpoint and returns a map
// [group: (IP, ...), ...]
func FetchNetbirdPeerGroups(apiURL, token string) (GroupPeers, error) {
	req, err := http.NewRequest(http.MethodGet, apiURL+"/peers", nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Token "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, body)
	}

	var peers []peer
	if err := json.NewDecoder(resp.Body).Decode(&peers); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	result := make(GroupPeers)
	for _, p := range peers {
		for _, g := range p.Groups {
			result[g.Name] = append(result[g.Name], p.IP)
		}
	}
	return result, nil
}
