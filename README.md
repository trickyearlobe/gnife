# GNIFE CLI

Gnife is an after-market command line client for Chef Infra server written in GoLang.
It's pretty lightweight and fast compared to the official `knife` utility from Progress (formerly Chef or Opscode).

Right now it's a work in progress so:-

* it doesn't have much of the functionality of the official client
* it doesn't have much in the way of error checking implemented yet
* it is probably full of terribly dangerous bugs


# GNIFE Installation

* You will need a working version of GO installed for your operating system.
* You will need a working version of GIT configured to be able to access github

First grab the code

```bash
# Cloning with SSH
git clone git@github.com:trickyearlobe/gnife.git
```

```bash
# Cloning with HTTPS
git clone https://github.com/trickyearlobe/gnife.git
```


Then build the code

```bash
cd gnife
go install
```

Then make sure that the installed binaries are in your `PATH`. You can check your GOPATH using

```bash
go env GOPATH
```

On my Mac I added this line to my .bash_profile in my home directory

```bash
export PATH="$PATH:$GOPATH/bin"
```


# Configuring

`gnife` uses the standard `~/.chef/credentials` file to define profiles, and uses the `~/.chef/context` file to select which profile is active.

`gnife` completely ignores the older style `client.rb`, `knife.rb` and `config.rb` files.

The credentials file usually looks something like this

```toml
[richard-prod]
client_name = "richard"
client_key = "/Users/richard/.chef/richard.api.chef.io.pem"
chef_server_url = "https://api.chef.io/organizations/richard-prod"
ssl_no_verify = false
cookbook_path = "~/repos/github/trickyearlobe/prod/cookbooks"

[richard-home]
client_name = "richard"
client_key = "/Users/richard/.chef/richard.chef.local.pem"
chef_server_url = "https://chef.local/organizations/richard-test"
ssl_no_verify = true
cookbook_path = "~/repos/github/trickyearlobe/test/cookbooks"
```

See [Setting up Knife - Profiles](https://docs.chef.io/workstation/knife_setup) for more info on using a `credentials` file.


# Using

Get a list of things gnife can do

```
richard@beastie > gnife

An after-market command line client for Chef Infra Server

Usage:
  gnife [command]

Available Commands:
  client      Commands for manipulating clients
  completion  Generate the autocompletion script for the specified shell
  help        Help about any command
  node        Commands for manipulating nodes
  raw         Perform raw API operations

Flags:
  -h, --help   help for gnife

Use "gnife [command] --help" for more information about a command.
```

Getting a list of nodes

```
gnife node list
```