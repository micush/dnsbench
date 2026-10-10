# Anyname DNS Director quick start (using the web GUI)

Anyname gives a group of machines one shared DNS address (a **gateway**). Clients point at that address; every node answers, and each query goes to the fastest healthy upstream DNS server you list. This guide takes you from the tarball to a working gateway using only the browser. The full reference is `README.md`.

## What you need

- One Linux machine to start with (Ubuntu, Debian, Fedora, RHEL, Rocky, Alma, Arch or Manjaro), with root access and internet access for the installer.
- A **free IP address** on the machine's network to use as the shared address (the VIP). It must not be in use by anything else. The machine itself keeps its own address.
- The address of at least one upstream DNS server (for example `8.8.8.8`) and a domain it can resolve (for example `google.com`).

## 1. Install

From GitHub, the latest tagged release:

```
curl -fsSL https://raw.githubusercontent.com/micush/ddgw/HEAD/get.sh | sudo bash
```

Or from a tarball:

```
tar xzf ddgw_v*.tgz
cd ddgw
sudo ./install.sh
```

The user running the installer (you, behind `sudo`) can log in to the GUI; add others with `--add-user NAME` or later on the Users page. The installer installs what is missing, builds Anyname, creates the `ddgw` group and the PAM login file, and starts the `ddgw` service. Add `--dry-run` first if you want to see what it will do. Re-running it on a newer tarball upgrades in place and keeps your settings.

Who can log in: a user whose Linux password PAM accepts **and** who is in the `ddgw` group. `root` is not special; add it to the group only if you want it to log in (Configure ▸ Users ▸ **Add existing user** adds an existing account to the group later).

## 2. Open the GUI

Browse to **https://&lt;this machine's address&gt;:53853** and sign in with that Linux user and password.

- Your browser will warn about the certificate: Anyname made a self-signed one. Continue for now; you will replace it in step 6.
- No sign-in possible? Check `groups alice` lists `ddgw`, and that `/etc/pam.d/ddgw` exists. A failed login shows one generic message on purpose.
- The page follows your light/dark setting. The **?** at the top right of every page opens help for that page.

## 3. Draw your first gateway (Topology)

A new install has no gateways. Topology is the first page.

1. **Right-click** the empty drawing and choose **New gateway…**
2. Fill in the form:
   - **Group number**: leave the suggestion (1).
   - **Name (optional)**: a label such as "Office DNS".
   - **Shared IPv4 address / prefix**: the free address with its prefix, for example `192.168.1.53/24`.
   - **Network interface**: the interface that is on that network (for example `eth0`).
3. Save. A **circle** appears: that is the gateway. It is amber at first because it has no DNS server yet. Everything you change is saved at once; there is no Apply button.
4. **Right-click the circle** and choose **Add DNS server…**
   - **IP address or host name**: for example `8.8.8.8` (add `:port` for a port other than 53, or `tls://host` for DNS over TLS).
   - **Domain to ask it about** and **Record type**: for example `google.com`, `A`. Anyname checks a server by asking it a real question.
5. A **square** (the server) and a **trapezoid** (the test domain) appear under the circle. When the gateway is working all three turn **green**.

Add a second server the same way for redundancy, and right-click a square to **Add domain…** so each server is tested with more than one name. Hover any shape for its status and uptime ("Online for 1m 1s - 0 failures").

Colours: **green** working, **yellow** degraded (the gateway runs but something is down), **red** down, **grey** not known yet. A server is only taken out of service when half or more of its test domains fail (change that on Configure ▸ DNS proxy).

## 4. Try it

From another machine on the network, ask the shared address a question:

```
dig @192.168.1.53 example.com
```

Then open **Monitor ▸ DNS** to see each server's rank, latency and how many queries it served. **Monitor ▸ Statistics** shows queries over time, top clients and top domains.

Point your clients (or your DHCP server) at the shared address when you are happy with it.

## 5. Add more nodes (optional, for redundancy)

Install Anyname on a second machine the same way (step 1 and 2), then:

1. On the first node: **Operate ▸ Cluster** ▸ **Create join code** and copy it. It works once and expires in an hour.
2. On the new node: **Operate ▸ Cluster** ▸ paste the code ▸ **Join**.
3. The new node takes over the shared settings (the drawing); only its interface, priority and similar per-node settings stay local. Check the **Network interface** for the gateway on that node (Topology ▸ right-click the circle ▸ **Edit gateway…**).

Both nodes now answer on the shared address and one of them is the controller (see **Monitor ▸ Gateways**). Open TCP port 53854 between the nodes. With a cluster, a drop-down at the top right lets you look at and configure any node from the one you are logged in to.

## 6. Replace the certificate

**Configure ▸ Web GUI** shows the certificate the GUI uses. Install your own (paste the certificate and key, or generate a signing request and paste the signed result) so your browser stops warning. The same certificate is used for DNS over TLS if you turn it on.

## 7. Worth knowing

- **General, Gateway groups, DNS proxy, Web GUI and Cluster** (all under Configure) hold everything else, and every field saves on its own: probe timing, cache, spread of queries over servers of similar speed, `down_percent`, DNS over TLS and DNS over HTTPS ports for clients, and more. Each page's **?** help lists the command-line equivalent.
- **History** keeps every saved change as a version. You can compare a version with the live one and restore any of them.
- **Operate ▸ Upgrade**: upload a newer Anyname tarball, tick the nodes to update and press the button in the Nodes card (none ticked = this node). A bad update rolls back by itself.
- **Operate ▸ Anycast**: switch BGP, or a single neighbor, off and on without losing the settings (set them under **Configure ▸ Anycast**).
- **Operate ▸ Node**: make this node the gateway controller, put it in maintenance (pause), or restart or shut down the host, now or later.
- Bind the GUI to a management address or firewall port 53853: it can reconfigure a daemon that runs as root.
- To start over on a node: stop the service, `sudo rm -rf /var/lib/ddgw/*`, start it again.
- To remove ddgw: `sudo ddgw-uninstall` (add `--purge` to delete its settings too).

## If something looks wrong

- **Circle stays grey or amber**: the node has no address on the gateway's subnet, or no server answers. Hover it: the tooltip says why.
- **Server red**: hover it (or look at the last error on **Monitor ▸ DNS**). A typo in the address, a blocked port 53 or a domain the server cannot resolve are the usual causes.
- **Log**: **Monitor ▸ Log** shows what the daemon is doing, filtered by level.
