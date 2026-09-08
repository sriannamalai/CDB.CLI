package couch

import "context"

// ServerInfo is the response of GET /.
type ServerInfo struct {
	Version  string
	Vendor   string
	Features []string
}

// SessionInfo is the response of GET /_session.
type SessionInfo struct {
	Name     string
	Roles    []string
	Method   string
	Handlers []string
}

// Ping checks that the server answers and reports its version.
func (c *Client) Ping(ctx context.Context) (ServerInfo, error) { return c.ServerInfo(ctx) }

// ServerInfo reads GET /.
func (c *Client) ServerInfo(ctx context.Context) (ServerInfo, error) {
	var body struct {
		Version  string   `json:"version"`
		Features []string `json:"features"`
		Vendor   struct {
			Name string `json:"name"`
		} `json:"vendor"`
	}
	if err := c.DoJSON(ctx, "GET", "/", nil, &body, "read", "server "+c.host); err != nil {
		return ServerInfo{}, err
	}
	return ServerInfo{Version: body.Version, Vendor: body.Vendor.Name, Features: body.Features}, nil
}

// Session reads GET /_session.
func (c *Client) Session(ctx context.Context) (SessionInfo, error) {
	var body struct {
		UserCtx struct {
			Name  string   `json:"name"`
			Roles []string `json:"roles"`
		} `json:"userCtx"`
		Info struct {
			Authenticated string   `json:"authenticated"`
			Handlers      []string `json:"authentication_handlers"`
		} `json:"info"`
	}
	if err := c.DoJSON(ctx, "GET", "/_session", nil, &body, "read", "session on "+c.host); err != nil {
		return SessionInfo{}, err
	}
	return SessionInfo{
		Name:     body.UserCtx.Name,
		Roles:    body.UserCtx.Roles,
		Method:   body.Info.Authenticated,
		Handlers: body.Info.Handlers,
	}, nil
}
