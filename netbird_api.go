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
	UserID string      `json:"user_id"`
}

// UserInfo contains information about a user
type UserInfo struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

// UserIDMap maps user IDs to user information
type UserIDMap map[string]UserInfo

// IPUserMap maps IP addresses to user IDs
type IPUserMap map[string]string

// GroupPeers maps each group name to the list of peer IPs that belong to it.
type GroupPeers map[string][]string

// makeAPIRequest creates and executes an HTTP request to the NetBird API
func makeAPIRequest(apiURL, token, endpoint string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, apiURL+endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Token "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, body)
	}

	return resp, nil
}

// FetchNetbirdUsers calls the NetBird API users endpoint and returns a map
// [userID: UserInfo]
func FetchNetbirdUsers(apiURL, token string) (UserIDMap, error) {
	resp, err := makeAPIRequest(apiURL, token, "/users")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var users []UserInfo
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	result := make(UserIDMap)
	for _, u := range users {
		result[u.ID] = u
	}
	return result, nil
}

// FetchNetbirdPeerGroups calls the NetBird API peers endpoint and returns a map
// [group: (IP, ...), ...] and a map of IP to user ID
func FetchNetbirdPeerGroups(apiURL, token string) (GroupPeers, IPUserMap, error) {
	resp, err := makeAPIRequest(apiURL, token, "/peers")
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	var peers []peer
	if err := json.NewDecoder(resp.Body).Decode(&peers); err != nil {
		return nil, nil, fmt.Errorf("decoding response: %w", err)
	}

	result := make(GroupPeers)
	ipUserMap := make(IPUserMap)
	for _, p := range peers {
		// Map IP to user ID
		if p.UserID != "" {
			ipUserMap[p.IP] = p.UserID
		}

		// Add IP to each group it belongs to
		for _, g := range p.Groups {
			result[g.Name] = append(result[g.Name], p.IP)
		}
	}
	return result, ipUserMap, nil
}
