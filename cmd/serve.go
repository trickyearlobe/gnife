package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/trickyearlobe/gnife/internal/serve"
)

func newServeCmd(a *app) *cobra.Command {
	var opts serve.Options
	var quietLog bool
	c := &cobra.Command{
		Use:   "serve --dir DIR",
		Short: "Run a Chef Infra Server over a backup directory",
		Long: `Serve DIR — a directory in the knife-ec-backup layout, such as one written
by "gnife backup" — as a Chef Infra Server. knife, chef-client and gnife can
talk to it; every change is written straight back into DIR, so the
directory is always a valid backup of the served state.

An empty or missing DIR starts a fresh server: a pivotal superuser is
created and its key written to DIR/pivotal.pem. A DIR that came from a real
server keeps that server's users and keys, so the same pivotal.pem and
client keys work. --org creates an organisation (with DIR/ORG-validator.pem)
if it is not already present.

The server enforces authentication (signing protocols 1.0 to 1.3) but not
authorisation: any valid key may do anything. It is for development,
testing and rehearsing restores, not for production.

  gnife serve --dir ./thenixons-backup
  gnife serve --dir ./scratch --org dev --tls --listen 0.0.0.0:8443`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Version = a.build.Version
			opts.Infof = func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
			if !quietLog {
				opts.Logf = func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
			}
			return serve.Run(cmd.Context(), opts)
		},
	}
	c.Flags().StringVar(&opts.Dir, "dir", "", "directory to serve (knife-ec-backup layout; created if missing)")
	_ = c.MarkFlagRequired("dir")
	c.Flags().StringVar(&opts.Listen, "listen", "127.0.0.1:8889", "address to listen on")
	c.Flags().StringVar(&opts.Org, "org", "", "create this organisation if DIR does not have it")
	c.Flags().BoolVar(&opts.TLS, "tls", false, "serve HTTPS with a self-signed certificate kept in DIR (tls.crt, tls.key)")
	c.Flags().StringVar(&opts.CertFile, "tls-cert", "", "serve HTTPS with this certificate")
	c.Flags().StringVar(&opts.KeyFile, "tls-key", "", "private key for --tls-cert")
	c.Flags().BoolVar(&quietLog, "no-log", false, "do not log requests")
	return c
}
