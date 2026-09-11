// Package localapp shares the local node discovery format with the desktop
// launcher. No private wallet material is stored in this endpoint file.
package localapp

import (
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"go.sia.tech/core/types"
)

type Endpoint struct {
	Format  int           `json:"format"`
	URL     string        `json:"url"`
	PID     int           `json:"pid"`
	Genesis types.BlockID `json:"genesis"`
}

func (e Endpoint) Validate() error {
	u, err := url.Parse(e.URL)
	if err != nil || e.Format != 1 || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid local node endpoint")
	}
	host, port, err := net.SplitHostPort(u.Host)
	n, parseErr := strconv.Atoi(port)
	if err != nil || parseErr != nil || n < 1 || n > 65535 || host != "127.0.0.1" {
		return errors.New("node endpoint must be a literal loopback address")
	}
	return nil
}

func ReadEndpoint(dir string) (e Endpoint, err error) {
	b, err := os.ReadFile(filepath.Join(dir, "node.json"))
	if err != nil {
		return e, err
	}
	if len(b) > 4096 {
		return e, errors.New("invalid endpoint file size")
	}
	if err = json.Unmarshal(b, &e); err != nil {
		return e, err
	}
	return e, e.Validate()
}

func WriteEndpoint(dir string, e Endpoint) error {
	if err := e.Validate(); err != nil {
		return err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".node-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err = tmp.Write(b); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, "node.json"))
}
