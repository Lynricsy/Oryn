# Orb inspection report (2026-11-12T10:46:40+00:00)

## 1. OS

```console
$ cat /etc/os-release
PRETTY_NAME="Debian GNU/Linux 12 (bookworm)"
NAME="Debian GNU/Linux"
VERSION_ID="12"
VERSION="12 (bookworm)"
VERSION_CODENAME=bookworm
ID=debian
HOME_URL="https://www.debian.org/"
SUPPORT_URL="https://www.debian.org/support"
BUG_REPORT_URL="https://bugs.debian.org/"
```

```console
$ uname -a
Linux 8583a5239efa 7.3.0-5-cachyos #1 SMP PREEMPT_DYNAMIC Fri, 23 Oct 2026 14:38:02 +0000 x86_64 GNU/Linux
```

```console
$ nproc
24
```

```console
$ free -h
               total        used        free      shared  buff/cache   available
Mem:            45Gi        30Gi       6.4Gi        62Mi       8.9Gi        14Gi
Swap:           59Gi        36Gi        23Gi
```

```console
$ df -h
Filesystem      Size  Used Avail Use% Mounted on
overlay         2.0T  569G  1.4T  29% /
tmpfs            64M     0   64M   0% /dev
shm             1.0G     0  1.0G   0% /dev/shm
/dev/sda3       2.0T  569G  1.4T  29% /usr/sbin/docker-init
tmpfs           4.0K     0  4.0K   0% /proc/acpi
```

```console
$ mount | head -40
overlay on / type overlay (rw,relatime,lowerdir=/var/lib/docker/overlay2/l/6FO2PKYQ7VU3N3LE42EK7QR4XA:/var/lib/docker/overlay2/l/O4CPJQ6JF4HLTWNAZIM6UKNGQE:/var/lib/docker/overlay2/l/KSQR3I4RV3CNGUV5N4D6CHQWTQ:/var/lib/docker/overlay2/l/EBKPHS5MNIKZBRRHEICRWULGII:/var/lib/docker/overlay2/l/UYHZUYN6F5WRP5GXQG56N2EOYR:/var/lib/docker/overlay2/l/PZYQ3SNQF7ST6PFC7OUTJRSWK2:/var/lib/docker/overlay2/l/O7FHOQ76XT2NN52U7JDFIKDP6U:/var/lib/docker/overlay2/l/OC2MBQOXTUHA4MPZKZZBKVLG5O:/var/lib/docker/overlay2/l/6UPN3VBMHQFJDADWW2P56MHW2M:/var/lib/docker/overlay2/l/PW6JMUKSLK7DXZR7TXE6M3LPKE:/var/lib/docker/overlay2/l/2EBA7RS7M75KNU2XWEFBPWB2UJ:/var/lib/docker/overlay2/l/QT7OYQP5IRVC5IE6HBVEMF5TXS:/var/lib/docker/overlay2/l/YQDK5SZIGGFXHERXVFCJ27BNIZ:/var/lib/docker/overlay2/l/K5RJHT5AXHXZSERPAP3C4HBGQ6:/var/lib/docker/overlay2/l/SBEFGEQJ3HDYV2QNWUHUSW6AHX:/var/lib/docker/overlay2/l/6GLLQJVNN4STICNNMX34FVBVXJ:/var/lib/docker/overlay2/l/MODOCR6BGL6BKZPQNU3YXV7T6M:/var/lib/docker/overlay2/l/LEB2NJ3BNUUEOAQ5BITV47AZB3:/var/lib/docker/overlay2/l/PIIOQM2INLAWNZGS25NL4MN6SL:/var/lib/docker/overlay2/l/L7KQ64UMRTIQMCQREDBO6BQJIQ:/var/lib/docker/overlay2/l/D2R3YNHZHQE6JQA3VHS2GYP6MP:/var/lib/docker/overlay2/l/FTUSM6VCCK5U3OA7PAVLMLXLB3:/var/lib/docker/overlay2/l/UC3EXKTO43YO4DU2CVHKSQ2IOZ:/var/lib/docker/overlay2/l/DKCDEVHMKASULXXLZHTXEU55H7:/var/lib/docker/overlay2/l/K4T55QAHDEFGJHRSNKDWYV76M6:/var/lib/docker/overlay2/l/XMA5LQCIF3PXLEDORSNJCQGIYR:/var/lib/docker/overlay2/l/TYDN6LLGNEFFL4U6J4LEQROHOV:/var/lib/docker/overlay2/l/QGVMNFK2L4G6J22A72S74GZGJS:/var/lib/docker/overlay2/l/GEHOIBH26XIDG6J5C33BWXS4FK:/var/lib/docker/overlay2/l/WDBNY3XFSKW3VJIPTMGW6VPIQT:/var/lib/docker/overlay2/l/HQJAFMS7E2XNVDNX4PNCVG5IEV:/var/lib/docker/overlay2/l/XBX7FKLXQUYYZGBKGGUE5IPE2Y,upperdir=/var/lib/docker/overlay2/4a8c05a812818c9887f0977d7337ad79ae69afcfe1b235781f574f58962d8c9d/diff,workdir=/var/lib/docker/overlay2/4a8c05a812818c9887f0977d7337ad79ae69afcfe1b235781f574f58962d8c9d/work,index=off)
proc on /proc type proc (rw,nosuid,nodev,noexec,relatime)
tmpfs on /dev type tmpfs (rw,nosuid,size=65536k,mode=755,inode64,huge=advise)
devpts on /dev/pts type devpts (rw,nosuid,noexec,relatime,gid=5,mode=620,ptmxmode=666)
sysfs on /sys type sysfs (ro,nosuid,nodev,noexec,relatime)
cgroup on /sys/fs/cgroup type cgroup2 (ro,nosuid,nodev,noexec,relatime,nsdelegate,memory_recursiveprot,memory_hugetlb_accounting)
mqueue on /dev/mqueue type mqueue (rw,nosuid,nodev,noexec,relatime)
shm on /dev/shm type tmpfs (rw,nosuid,nodev,noexec,relatime,size=1048576k,inode64,huge=advise)
/dev/sda3 on /usr/sbin/docker-init type btrfs (ro,relatime,compress=zstd:1,ssd,discard=async,space_cache=v2,subvolid=5,subvol=/)
/dev/sda3 on /etc/hostname type btrfs (rw,relatime,compress=zstd:1,ssd,discard=async,space_cache=v2,subvolid=5,subvol=/)
/dev/sda3 on /etc/hosts type btrfs (rw,relatime,compress=zstd:1,ssd,discard=async,space_cache=v2,subvolid=5,subvol=/)
/dev/sda3 on /etc/resolv.conf type btrfs (rw,relatime,compress=zstd:1,ssd,discard=async,space_cache=v2,subvolid=5,subvol=/)
proc on /proc/bus type proc (ro,nosuid,nodev,noexec,relatime)
proc on /proc/fs type proc (ro,nosuid,nodev,noexec,relatime)
proc on /proc/irq type proc (ro,nosuid,nodev,noexec,relatime)
proc on /proc/sys type proc (ro,nosuid,nodev,noexec,relatime)
proc on /proc/sysrq-trigger type proc (ro,nosuid,nodev,noexec,relatime)
tmpfs on /proc/acpi type tmpfs (ro,relatime,size=4k,nr_inodes=1,inode64,huge=advise)
tmpfs on /proc/asound type tmpfs (ro,relatime,size=4k,nr_inodes=1,inode64,huge=advise)
tmpfs on /proc/interrupts type tmpfs (rw,nosuid,size=65536k,mode=755,inode64,huge=advise)
tmpfs on /proc/kcore type tmpfs (rw,nosuid,size=65536k,mode=755,inode64,huge=advise)
tmpfs on /proc/keys type tmpfs (rw,nosuid,size=65536k,mode=755,inode64,huge=advise)
tmpfs on /proc/scsi type tmpfs (ro,relatime,size=4k,nr_inodes=1,inode64,huge=advise)
tmpfs on /proc/timer_list type tmpfs (rw,nosuid,size=65536k,mode=755,inode64,huge=advise)
tmpfs on /sys/firmware type tmpfs (ro,relatime,size=4k,nr_inodes=1,inode64,huge=advise)
```

```console
$ whoami; id; echo "$HOME $PWD $SHELL"
user
uid=1000(user) gid=1000(user) groups=1000(user),27(sudo),100(users),994(docker)
/home/user /home/user/workspace /bin/bash
```

```console
$ sudo -n true && echo sudo-ok || echo sudo-not-ok
sudo-ok
```


## 2. Processes

```console
$ ps -eo pid,ppid,user,etime,args --forest | cut -c1-260
    PID    PPID USER         ELAPSED COMMAND
      8       0 user           00:51 /usr/local/bin/amp orb bootstrap
      1       0 user           00:51 /sbin/docker-init -- sleep infinity
      7       1 user           00:51 sleep infinity
    213       1 user           00:45 /bin/bash /home/user/.amp/http-headless-supervisor.sh
    262     213 user           00:45  \_ /home/user/.amp/bin/amp --dangerously-allow-all --headless=T-01a1cf5c-fc45-713b-98e9-b5d19612cc45 --log-file=/home/user/.cache/amp/logs/headless.log
    314     262 user           00:44      \_ /home/user/.amp/bin/amp run /tmp/amp-plugin-runtime-nBpnNJ/plugin-runtime.ts /home/user/.cache/amp/global-plugins/ampcode.com/user/lsp-diagnostics@5df0516ecf2ab5e0/index.ts
    315     262 user           00:44      \_ /home/user/.amp/bin/amp run /tmp/amp-plugin-runtime-TMdxx8/plugin-runtime.ts /home/user/.cache/amp/global-plugins/ampcode.com/user/advisor@0d004ff417116003/index.ts
    316     262 user           00:44      \_ /home/user/.amp/bin/amp run /tmp/amp-plugin-runtime-eAqWUJ/plugin-runtime.ts /home/user/.cache/amp/global-plugins/ampcode.com/user/project-memory@9dd0afc2865e3c82/index.ts
    317     262 user           00:44      \_ /home/user/.amp/bin/amp run /tmp/amp-plugin-runtime-PW2LgB/plugin-runtime.ts /home/user/.cache/amp/global-plugins/ampcode.com/user/code-tools@3c693b88e210a54d/index.ts
    318     262 user           00:44      \_ /home/user/.amp/bin/amp run /tmp/amp-plugin-runtime-F8dxAt/plugin-runtime.ts /home/user/.cache/amp/global-plugins/ampcode.com/user/turn-control@0df4c6e2343c1739/index.ts
    319     262 user           00:44      \_ /home/user/.amp/bin/amp run /tmp/amp-plugin-runtime-FZRZVt/plugin-runtime.ts /home/user/.cache/amp/global-plugins/ampcode.com/user/guards-and-rules@add350b238fc8613/index.ts
    400     262 user           00:42      \_ bunx --no-cache mcp-agentlogs@latest
    459     400 user           00:41      |   \_ node /home/user/.bun/install/cache/.bunx-1000/mcp-agentlogs@latest/node_modules/.bin/mcp-agentlogs
    402     262 user           00:42      \_ bunx -y @upstash/context7-mcp@latest --api-key ${CONTEXT7_API_KEY}
    470     402 user           00:41      |   \_ node /home/user/.bun/install/cache/.bunx-1000/@upstash/context7-mcp@latest/node_modules/.bin/context7-mcp --api-key ${CONTEXT7_API_KEY}
    403     262 user           00:42      \_ npm exec @sammysnake/fast-context-mcp
    489     403 user           00:38      |   \_ sh -c fast-context-mcp
    490     489 user           00:38      |       \_ node /home/user/.npm/_npx/bbe0f4a3dc7c0fcb/node_modules/.bin/fast-context-mcp
    536     262 user           00:00      \_ /bin/bash -c bash /tmp/orb-inspect.sh; wc -l ~/orb-report.md
    537     536 user           00:00          \_ bash /tmp/orb-inspect.sh
    561     537 user           00:00              \_ bash -c ps -eo pid,ppid,user,etime,args --forest | cut -c1-260
    563     561 user           00:00              |   \_ ps -eo pid,ppid,user,etime,args --forest
    564     561 user           00:00              |   \_ cut -c1-260
    562     537 user           00:00              \_ head -80
```

```console
$ tr '\0' ' ' < /proc/1/cmdline; echo
/sbin/docker-init -- sleep infinity 
```


## 3. Sockets

```console
$ ss -ltnpu
bash: line 1: ss: command not found
```

```console
$ ss -tnp | head -40
bash: line 1: ss: command not found
```


## 4. Env

```console
$ env | cut -d= -f1 | sort | tr '\n' ' '; echo
AGENT AGENT_THREAD_ID AI_AGENT AMP_BIN_DIR AMP_CURRENT_THREAD_ID AMP_DIRECT_DESKTOP AMP_DIRECT_DESKTOP_HOST AMP_DIRECT_NET_HOST AMP_DIRECT_TERMINAL AMP_DIRECT_TERMINAL_HOST AMP_DISABLE_AMP_COAUTHOR_TRAILER AMP_EXECUTOR AMP_GIT_CREDENTIAL_HOSTS AMP_ORB AMP_ORB_PROVIDER AMP_PORTAL_DOMAIN AMP_SECRET_ENV_NAMES AMP_THREAD_ID AMP_THREAD_IDENTITY_AUTH AMP_URL AMP_USER_EMAIL AMP_USER_ID AMP_WORKING_DIRECTORY AMP_WORKLOAD_IDENTITY_REQUEST_TOKEN AMP_WORKSPACE_ID CLICOLOR CLICOLOR_FORCE DEBIAN_FRONTEND GH_HOST GPG_KEY HOME HOSTNAME LANG LD_LIBRARY_PATH NIX_PROFILES NIX_SSL_CERT_FILE NODE_OPTIONS NODE_VERSION NONINTERACTIVE NO_COLOR OLDPWD PATH PWD PYTHON_GET_PIP_SHA256 PYTHON_GET_PIP_URL PYTHON_PIP_VERSION PYTHON_SETUPTOOLS_VERSION PYTHON_VERSION SHLVL TERM USER XDG_DATA_DIRS _ 
```

```console
$ env | grep -E '^(AMP_|ORB_|E2B_)' | sort | awk -F= '{n=$1; if (n ~ /TOKEN|KEY|SECRET|PASSWORD|COOKIE/) print n"=<REDACTED>"; else print}'
AMP_BIN_DIR=/home/user/.amp/bin
AMP_CURRENT_THREAD_ID=T-01a1cf5c-fc45-713b-98e9-b5d19612cc45
AMP_DIRECT_DESKTOP=1
AMP_DIRECT_DESKTOP_HOST=127.0.0.1
AMP_DIRECT_NET_HOST=127.0.0.1
AMP_DIRECT_TERMINAL=1
AMP_DIRECT_TERMINAL_HOST=127.0.0.1
AMP_DISABLE_AMP_COAUTHOR_TRAILER=1
AMP_EXECUTOR=sandbox
AMP_GIT_CREDENTIAL_HOSTS=gitlab.com
AMP_ORB=1
AMP_ORB_PROVIDER=orb-provider
AMP_PORTAL_DOMAIN=onamp.dev
AMP_SECRET_ENV_NAMES=<REDACTED>
AMP_THREAD_ID=T-01a1cf5c-fc45-713b-98e9-b5d19612cc45
AMP_THREAD_IDENTITY_AUTH=1
AMP_URL=https://ampcode.com
AMP_USER_EMAIL=im@ling.plus
AMP_USER_ID=user_01KD21N4TNZYZQ8D71K2GTQ63J
AMP_WORKING_DIRECTORY=/home/user/workspace
AMP_WORKLOAD_IDENTITY_REQUEST_TOKEN=<REDACTED>
AMP_WORKSPACE_ID=
```


## 5. Amp install

```console
$ which amp; amp --version
/home/user/.amp/bin/amp
0.0.1794470450-g3a6702 (released 2026-11-12T08:00:50.000Z, 2h ago)
```

```console
$ ls -la ~/.amp ~/.config/amp ~/.local/share/amp ~/.cache/amp
/home/user/.amp:
total 20
drwx------ 1 user user  252 Nov 12 10:45 .
drwxr-xr-x 1 user user  190 Nov 12 10:46 ..
-rw-r--r-- 1 user user 4984 Nov 12 10:45 amp-headless-update-binary
drwx------ 1 user user   62 Nov 12 10:45 bin
-rw-r--r-- 1 user user    4 Nov 12 10:45 http-headless-supervisor.pid
-rw-r--r-- 1 user user 1304 Nov 12 10:45 http-headless-supervisor.sh
-rw-r--r-- 1 user user    4 Nov 12 10:45 http-headless.pid

/home/user/.cache/amp:
total 0
drwx------ 1 user user 152 Nov 12 10:45 .
drwx------ 1 user user   6 Nov 12 10:45 ..
drwxr-xr-x 1 user user  22 Nov 12 10:45 global-plugins
drwxr-xr-x 1 user user  22 Nov 12 10:45 global-skill-manifests
drwxr-xr-x 1 user user  22 Nov 12 10:45 global-skills
drwx------ 1 user user  88 Nov 12 10:45 logs
drwx------ 1 user user 128 Nov 12 10:45 pids
drwxr-xr-x 1 user user  56 Nov 12 10:45 plugin-cache
drwxr-xr-x 1 user user   0 Nov 12 10:45 profile

/home/user/.config/amp:
total 4
drwx------ 1 user user    0 Nov  6 20:54 .
drwxr-xr-x 1 user user    6 Nov  6 20:54 ..
-rw-r--r-- 1 user user 1547 Nov  6 20:54 AGENTS.md

/home/user/.local/share/amp:
total 16
drwx------ 1 user user 98 Nov 12 10:45 .
drwxr-xr-x 1 user user  6 Nov  6 20:54 ..
-rw------- 1 user user 34 Nov 12 10:45 .legacy-state-owner.json
drwxr-xr-x 1 user user  6 Nov 12 10:45 bin
-rw------- 1 user user 62 Nov 12 10:45 device-id.json
-rw-r--r-- 1 user user 37 Nov 12 10:45 guest-id
-rw-r--r-- 1 user user 33 Nov  6 20:54 session.json
```

```console
$ find / -maxdepth 4 \( -iname '*amp*' -o -iname '*orb*' \) -not -path '/proc/*' -not -path '/sys/*' -not -path '/usr/share/*' -not -path '/usr/lib/*' 2>/dev/null | head -80
/etc/systemd/system/amp-control.slice
/etc/systemd/system/amp-headless-bootstrap.service
/etc/systemd/system/amp-headless-env.path
/etc/systemd/system/amp-headless-env.service
/etc/systemd/system/amp-headless.service
/etc/systemd/system/amp-workload.slice
/etc/systemd/system/amp.slice
/etc/gitconfig.d/amp-allowed-signers
/etc/gitconfig.d/amp-signing-off
/etc/gitconfig.d/amp-signing
/etc/sudoers.d/amp-systemd-run
/etc/alsa/conf.d/10-samplerate.conf
/etc/alsa/conf.d/99-pulseaudio-default.conf.example
/etc/ssl/certs/XRamp_Global_CA_Root.pem
/home/user/.cache/amp
/home/user/.config/amp
/home/user/.amp
/home/user/.amp/amp-headless-update-binary
/home/user/orb-report.md
/opt/amp-wayland
/run/amp
/tmp/amp-e459640a-fa91-4da0-b0c2-df3bd57fb63c.sh
/tmp/oh-my-amp-result-bus
/tmp/orb-inspect.sh
/usr/bin/uclampset
/usr/include/OpenEXR/ImfSampleCountChannel.h
/usr/include/linux/net_tstamp.h
/usr/include/linux/psample.h
/usr/local/bin/amp-sign-commit
/usr/local/bin/amp
/usr/local/bin/amp-run-workload
/usr/local/bin/amp-validate-workload-cgroups
/usr/local/bin/amp-headless-apply-env
/usr/local/bin/amp-headless-update-binary
/usr/sbin/pam_timestamp_check
/nix/store/xmmxg0vbkvampcsz2rp792nj0s65iv5r-aws-c-cal-0.9.2
```


## 6. Workspace

```console
$ ls -la "$PWD"
total 8
drwxr-xr-x 1 user user  64 Nov 12 10:45 .
drwxr-xr-x 1 user user 190 Nov 12 10:46 ..
drwxr-xr-x 1 user user   0 Nov 12 10:45 .agent-logs
-rw-r--r-- 1 user user  13 Nov 12 10:45 .gitignore
-rw-r--r-- 1 user user 325 Nov 12 10:45 PROJECTS.md
```

```console
$ ls -la .amp .agents
ls: cannot access '.amp': No such file or directory
ls: cannot access '.agents': No such file or directory
```

```console
$ git status | head -5
fatal: not a git repository (or any of the parent directories): .git
```


## 7. Desktop

```console
$ amp orb desktop ensure
Error: amp orb desktop requires systemd and passwordless sudo, which are available inside Amp-managed orbs.
```

```console
$ echo "DISPLAY=$DISPLAY"
DISPLAY=
```

```console
$ ps -eo pid,args | grep -Ei 'xvfb|xvnc|vnc|chrome|novnc|websockify|wayland|weston|openbox|xfce|kasm|selkies' | grep -v grep | cut -c1-300
```


## 8. Services and portals

```console
$ amp orb service start demo --command 'python3 -m http.server "$PORT"' --portal --title Demo
demo: started, port 31262, listening
Manage with: amp orb service stop demo | amp orb service restart demo
Wrote portal manifest /home/user/workspace/.amp/portals/demo.json.
https://t-03h80q0rd6214ksqem1qfl9vp-p20629.onamp.dev/
```

```console
$ sleep 3; amp orb service list
amp-svc-demo active (running) pid=686 name=demo home=/home/user/workspace
amp-svc-portal-proxy-20629 active (running) pid=666
```

```console
$ amp orb service status demo
amp-svc-demo - python3 -m http.server "$PORT"
   Active: active (running) since 2026-11-12T10:46:41.301Z
   Supervisor PID: 680; Main PID: 686
   Working directory: /home/user/workspace
   Home: /home/user/workspace
   Log: /home/user/.cache/amp/logs/orb-services/demo.log

127.0.0.1 - - [12/Nov/2026 10:46:41] "HEAD / HTTP/1.1" 200 -
```

```console
$ amp orb service logs demo | tail -20
127.0.0.1 - - [12/Nov/2026 10:46:41] "HEAD / HTTP/1.1" 200 -
```

```console
$ ls -la .amp .amp/portals
.amp:
total 0
drwxr-xr-x 1 user user 14 Nov 12 10:46 .
drwxr-xr-x 1 user user 72 Nov 12 10:46 ..
drwxr-xr-x 1 user user 62 Nov 12 10:46 portals

.amp/portals:
total 12
drwxr-xr-x 1 user user  62 Nov 12 10:46 .
drwxr-xr-x 1 user user  14 Nov 12 10:46 ..
-rw-r--r-- 1 user user 151 Nov 12 10:46 .proxies
-rw-r--r-- 1 user user  46 Nov 12 10:46 .service-ports
-rw-r--r-- 1 user user 177 Nov 12 10:46 demo.json
```

```console
$ cat .amp/portals/*.json
{
	"links": [
		{
			"label": "Demo",
			"url": "https://t-03h80q0rd6214ksqem1qfl9vp-p20629.onamp.dev/"
		}
	],
	"amp": {
		"owner": "amp-orb-service",
		"service": "demo"
	}
}
```

```console
$ amp orb portal list
No portals have a custom hostname or public access.
```

```console
$ echo PORTAL_URL=https://t-03h80q0rd6214ksqem1qfl9vp-p20629.onamp.dev/; curl -sS -o /dev/null -D - 'https://t-03h80q0rd6214ksqem1qfl9vp-p20629.onamp.dev/' | grep -vi cookie
PORTAL_URL=https://t-03h80q0rd6214ksqem1qfl9vp-p20629.onamp.dev/
HTTP/2 302 
location: https://ampcode.com/api/portal-auth/authorize?thread_id=T-01a1cf5c-fc45-713b-98e9-b5d19612cc45&port=20629&return_to=https%3A%2F%2Ft-03h80q0rd6214ksqem1qfl9vp-p20629.onamp.dev%2F&state=Av96gSRQpK3WqmEtM2fCytJ6zBB-wVYy
date: Thu, 12 Nov 2026 10:46:45 GMT
via: 1.1 google
content-length: 0
alt-svc: h3=":443"; ma=2592000

```


## 9. tmux

```console
$ tmux ls
error connecting to /tmp/tmux-1000/default (No such file or directory)
```

```console
$ tmux list-panes -a
error connecting to /tmp/tmux-1000/default (No such file or directory)
```

```console
$ tmux -L amp-terminal ls; tmux -L amp-terminal list-panes -a
error connecting to /tmp/tmux-1000/amp-terminal (No such file or directory)
error connecting to /tmp/tmux-1000/amp-terminal (No such file or directory)
```


## 10. Network

```console
$ ip -br a
bash: line 1: ip: command not found
```

```console
$ ip route
bash: line 1: ip: command not found
```

```console
$ cat /etc/resolv.conf
# Generated by Docker Engine.
# This file can be edited; Docker Engine will not make further changes once it
# has been modified.

nameserver 1.1.1.1
nameserver 8.8.8.8
search ling.plus

# Based on host file: '/run/systemd/resolve/resolv.conf' (legacy)
# Overrides: []
```

```console
$ curl -s --max-time 10 https://api.ipify.org; echo
64.20.45.163
```


## 11. Preinstalled tools

```console
$ for c in git node npm pnpm bun deno python3 pip uv go rustc cargo java docker gh rg fd jq tmux google-chrome chromium agent-browser playwright ffmpeg code; do printf "%s: " $c; command -v $c || echo MISSING; done
git: /opt/git/bin/git
node: /usr/local/bin/node
npm: /usr/local/bin/npm
pnpm: /usr/local/bin/pnpm
bun: /usr/local/bin/bun
deno: MISSING
python3: /usr/local/bin/python3
pip: /usr/local/bin/pip
uv: /usr/local/bin/uv
go: MISSING
rustc: MISSING
cargo: MISSING
java: MISSING
docker: /usr/bin/docker
gh: /home/user/.amp/bin/gh
rg: /usr/local/bin/rg
fd: MISSING
jq: /usr/bin/jq
tmux: /usr/bin/tmux
google-chrome: MISSING
chromium: MISSING
agent-browser: /usr/local/bin/agent-browser
playwright: MISSING
ffmpeg: /usr/bin/ffmpeg
code: MISSING
```


## 12. How you run (shell ancestry)

```console
$ p=$$; while [ -n "$p" ] && [ "$p" -ge 1 ] 2>/dev/null; do ps -o pid=,ppid=,args= -p $p | cut -c1-260; [ "$p" = 1 ] && break; p=$(ps -o ppid= -p $p | tr -d " "); done
    813     537 bash -c p=$$; while [ -n "$p" ] && [ "$p" -ge 1 ] 2>/dev/null; do ps -o pid=,ppid=,args= -p $p | cut -c1-260; [ "$p" = 1 ] && break; p=$(ps -o ppid= -p $p | tr -d " "); done
    537     536 bash /tmp/orb-inspect.sh
    536     262 /bin/bash -c bash /tmp/orb-inspect.sh; wc -l ~/orb-report.md
    262     213 /home/user/.amp/bin/amp --dangerously-allow-all --headless=T-01a1cf5c-fc45-713b-98e9-b5d19612cc45 --log-file=/home/user/.cache/amp/logs/headless.log
    213       1 /bin/bash /home/user/.amp/http-headless-supervisor.sh
      1       0 /sbin/docker-init -- sleep infinity
```

```console
$ ps -eo pid,ppid,etime,args | grep -E '[a]mp' | cut -c1-260
      8       0       00:56 /usr/local/bin/amp orb bootstrap
    213       1       00:50 /bin/bash /home/user/.amp/http-headless-supervisor.sh
    262     213       00:50 /home/user/.amp/bin/amp --dangerously-allow-all --headless=T-01a1cf5c-fc45-713b-98e9-b5d19612cc45 --log-file=/home/user/.cache/amp/logs/headless.log
    314     262       00:49 /home/user/.amp/bin/amp run /tmp/amp-plugin-runtime-nBpnNJ/plugin-runtime.ts /home/user/.cache/amp/global-plugins/ampcode.com/user/lsp-diagnostics@5df0516ecf2ab5e0/index.ts
    315     262       00:49 /home/user/.amp/bin/amp run /tmp/amp-plugin-runtime-TMdxx8/plugin-runtime.ts /home/user/.cache/amp/global-plugins/ampcode.com/user/advisor@0d004ff417116003/index.ts
    316     262       00:49 /home/user/.amp/bin/amp run /tmp/amp-plugin-runtime-eAqWUJ/plugin-runtime.ts /home/user/.cache/amp/global-plugins/ampcode.com/user/project-memory@9dd0afc2865e3c82/index.ts
    317     262       00:49 /home/user/.amp/bin/amp run /tmp/amp-plugin-runtime-PW2LgB/plugin-runtime.ts /home/user/.cache/amp/global-plugins/ampcode.com/user/code-tools@3c693b88e210a54d/index.ts
    318     262       00:49 /home/user/.amp/bin/amp run /tmp/amp-plugin-runtime-F8dxAt/plugin-runtime.ts /home/user/.cache/amp/global-plugins/ampcode.com/user/turn-control@0df4c6e2343c1739/index.ts
    319     262       00:49 /home/user/.amp/bin/amp run /tmp/amp-plugin-runtime-FZRZVt/plugin-runtime.ts /home/user/.cache/amp/global-plugins/ampcode.com/user/guards-and-rules@add350b238fc8613/index.ts
    660       1       00:04 /bin/bash /home/user/.local/share/amp/orb-services/supervisor.sh /home/user/.local/share/amp/orb-services/portal-proxy-20629 /home/user/.cache/amp/logs/orb-services/portal-proxy-20629.log /bin/bash
    666     660       00:04 node --input-type=module --eval import { readFileSync } from 'node:fs' import http from 'node:http' import https from 'node:https' import { pipeline } from 'node:stream' import { urlToHttpOptions } from 'node:url' import zlib from '
    680       1       00:04 /bin/bash /home/user/.local/share/amp/orb-services/supervisor.sh /home/user/.local/share/amp/orb-services/demo /home/user/.cache/amp/logs/orb-services/demo.log /bin/bash
```

```console
$ ss -tnp | grep -Ei 'amp|node|bun' | head -40
bash: line 1: ss: command not found
```


## 13. Metrics / profile / net

```console
$ amp orb system-metrics "$AMP_THREAD_ID" 2>&1 | tail -25
# Orb System Metrics

Thread: `T-01a1cf5c-fc45-713b-98e9-b5d19612cc45`

## Orb `orb-provider:afd29be2-a4ea-4005-8c39-5ee704c91903:amp-01a1cf5c-fc45-713b-98e9-b5d19612cc45`

Samples: 2

| CPU | Memory | Cache | Disk |
| ---: | ---: | ---: | ---: |
| 0.00% / 24 cores | 31686 MiB / 46727 MiB (67.8%) | 0 MiB | 584409 MiB / 2047698 MiB (28.5%) |
| 6.23% / 24 cores | 31684 MiB / 46727 MiB (67.8%) | 0 MiB | 584484 MiB / 2047698 MiB (28.5%) |
```

```console
$ amp profile status
No profile.
```

```console
$ amp net status
Orb-wide proxy configuration: off (http://127.0.0.1:58324, socks5h://127.0.0.1:58324)
Exit node: none connected (traffic through the proxy uses the orb’s own network until one connects)
Streams: 0 active, 0 through the exit node, 0 direct from the orb; 0 B sent, 0 B received
```

```console
$ amp orb services --help | head -30
Usage: amp orb service [options] [command]

Manage long-lived services (dev servers, watchers) as supervised processes that survive Amp CLI updates and restart on failure

Commands:

  ensure   Ensure every service declared in .amp/services.yaml is running and exposed at its portal URLs
  start    Start an undeclared ad-hoc supervised service, e.g.: amp orb service start preview --command 'pnpm dev'
           --portal
  restart  Restart a declared service from .amp/services.yaml or reload an existing ad-hoc service
  stop     Stop a service
  status   Show supervisor status for a service
  logs     Show recent logs for a service
  list     List orb services

Global options:

  --visibility <visibility>
      Set thread visibility (private, unlisted, workspace, group)
  -V, --version
      Print the version number and exit
  -v
      Alias for --version
  --settings-file <value>
      Custom settings file path (overrides the default location /home/user/.config/amp/settings.json)
  --log-level <value>
      Set log level (parent, children, category, sinks, parentSinks, filters, lowestLevel, contextLocalStorage)
  --log-file <value>
      Set log file location (overrides the default location /home/user/.cache/amp/logs/cli.log)
  --mcp-config <value>
```


## 14. Guidance files

```console
$ cat /home/user/.config/amp/AGENTS.md
# Secure Sandbox Environment

- You are running inside a secure E2B sandbox.
- Treat environment variables, git credentials, and secret files as sensitive; do not print full secret values.
- Run dev servers and other long-lived services with `amp orb service start <name> --command '<command>'` so they survive Amp CLI updates and orb pause/resume. Inspect with `amp orb service list` / `status <name>` / `logs <name>`; stop with `amp orb service stop <name>`.
- Do NOT rely on `nohup`, `setsid`, `&`, or tmux to keep servers alive: every process started from the Amp CLI shares its service cgroup and is killed when the Amp CLI updates or restarts (e.g. after the orb resumes from pause).
- Prefer `amp orb services ensure` when `.amp/services.yaml` declares the repository's services. Otherwise, use `amp orb service start <name> --command '<command>' --portal` to run and expose a server.
- Use tmux session `dev` for interactive background work you will attach to (REPLs, watch tasks); create a new window per task.
- If `dev` is missing, create it with `tmux new-session -d -s dev -c /home/user/workspace/repo`, then open a new window with `tmux new-window -t dev -c /home/user/workspace/repo`.
- If `.agents/Procfile` exists and defines services, the sandbox runtime auto-starts them.
- Save visual artifacts (screenshots, recordings, and similar media) under `.amp/in/artifacts/`, and pass `agent-browser` their absolute path: it reads an argument starting with `.` as a CSS selector and saves the capture to a temporary directory instead.
```

```console
$ ls -la ~/.cache/amp 2>&1; find / -maxdepth 8 -path /proc -prune -o \( -name SKILL.md -path "*orb-setup*" \) -print 2>/dev/null | head
total 0
drwx------ 1 user user 152 Nov 12 10:45 .
drwx------ 1 user user   6 Nov 12 10:45 ..
drwxr-xr-x 1 user user  22 Nov 12 10:45 global-plugins
drwxr-xr-x 1 user user  22 Nov 12 10:45 global-skill-manifests
drwxr-xr-x 1 user user  22 Nov 12 10:45 global-skills
drwx------ 1 user user 112 Nov 12 10:46 logs
drwx------ 1 user user 128 Nov 12 10:45 pids
drwxr-xr-x 1 user user  56 Nov 12 10:45 plugin-cache
drwxr-xr-x 1 user user   0 Nov 12 10:45 profile
```


## Supplement (ss/ip missing, so the same data was read from /proc)

### 3b. Sockets via /proc/net/{tcp,tcp6,udp,udp6} + /proc/<pid>/fd
```console
proto  state      local                                    remote                                   proc
tcp    LISTEN     127.0.0.1:58321                          0.0.0.0:0                                pid=262 /home/user/.amp/bin/amp --dangerously-allow-all --headless=T-01a1cf5c-fc45-713b-98e9-b5d19
tcp    LISTEN     127.0.0.1:58323                          0.0.0.0:0                                pid=262 /home/user/.amp/bin/amp --dangerously-allow-all --headless=T-01a1cf5c-fc45-713b-98e9-b5d19
tcp    LISTEN     127.0.0.1:58322                          0.0.0.0:0                                pid=262 /home/user/.amp/bin/amp --dangerously-allow-all --headless=T-01a1cf5c-fc45-713b-98e9-b5d19
tcp    LISTEN     127.0.0.1:58324                          0.0.0.0:0                                pid=262 /home/user/.amp/bin/amp --dangerously-allow-all --headless=T-01a1cf5c-fc45-713b-98e9-b5d19
tcp    LISTEN     0.0.0.0:20629                            0.0.0.0:0                                pid=666 node --input-type=module --eval import { readFileSync } from 'node:fs'
import http from 'n
tcp    LISTEN     127.0.0.1:9224                           0.0.0.0:0                                pid=262 /home/user/.amp/bin/amp --dangerously-allow-all --headless=T-01a1cf5c-fc45-713b-98e9-b5d19
tcp    LISTEN     127.0.0.1:9223                           0.0.0.0:0                                pid=262 /home/user/.amp/bin/amp --dangerously-allow-all --headless=T-01a1cf5c-fc45-713b-98e9-b5d19
tcp    LISTEN     0.0.0.0:31262                            0.0.0.0:0                                pid=686 python3 -m http.server 31262 
tcp    ESTAB      172.18.0.5:56016                         136.68.113.218:443                       pid=8 /usr/local/bin/amp orb bootstrap 
tcp    ESTAB      172.18.0.5:37032                         34.54.147.251:443                        pid=262 /home/user/.amp/bin/amp --dangerously-allow-all --headless=T-01a1cf5c-fc45-713b-98e9-b5d19
tcp    ESTAB      172.18.0.5:37044                         34.54.147.251:443                        pid=262 /home/user/.amp/bin/amp --dangerously-allow-all --headless=T-01a1cf5c-fc45-713b-98e9-b5d19
tcp    ESTAB      172.18.0.5:37056                         34.54.147.251:443                        pid=262 /home/user/.amp/bin/amp --dangerously-allow-all --headless=T-01a1cf5c-fc45-713b-98e9-b5d19
tcp    ESTAB      172.18.0.5:37062                         34.54.147.251:443                        pid=262 /home/user/.amp/bin/amp --dangerously-allow-all --headless=T-01a1cf5c-fc45-713b-98e9-b5d19
tcp    ESTAB      172.18.0.5:37066                         34.54.147.251:443                        pid=262 /home/user/.amp/bin/amp --dangerously-allow-all --headless=T-01a1cf5c-fc45-713b-98e9-b5d19
tcp    ESTAB      172.18.0.5:37078                         34.54.147.251:443                        pid=262 /home/user/.amp/bin/amp --dangerously-allow-all --headless=T-01a1cf5c-fc45-713b-98e9-b5d19
tcp    ESTAB      172.18.0.5:60416                         34.54.147.251:443                        pid=8 /usr/local/bin/amp orb bootstrap 
tcp    ESTAB      172.18.0.5:60428                         34.54.147.251:443                        pid=8 /usr/local/bin/amp orb bootstrap 
tcp    ESTAB      172.18.0.5:33562                         104.16.11.34:443                         pid=403 npm exec @sammysnake/fast-context-mcp                                   
tcp    ESTAB      172.18.0.5:33570                         104.16.11.34:443                         pid=403 npm exec @sammysnake/fast-context-mcp                                   
tcp    ESTAB      172.18.0.5:33574                         104.16.11.34:443                         pid=403 npm exec @sammysnake/fast-context-mcp                                   
tcp    ESTAB      172.18.0.5:33590                         104.16.11.34:443                         pid=403 npm exec @sammysnake/fast-context-mcp                                   
tcp    ESTAB      172.18.0.5:33598                         104.16.11.34:443                         pid=403 npm exec @sammysnake/fast-context-mcp                                   
tcp    ESTAB      172.18.0.5:33608                         104.16.11.34:443                         pid=403 npm exec @sammysnake/fast-context-mcp                                   
tcp    ESTAB      172.18.0.5:33624                         104.16.11.34:443                         pid=403 npm exec @sammysnake/fast-context-mcp                                   
tcp    ESTAB      172.18.0.5:33638                         104.16.11.34:443                         pid=403 npm exec @sammysnake/fast-context-mcp                                   
tcp    ESTAB      172.18.0.5:33642                         104.16.11.34:443                         pid=403 npm exec @sammysnake/fast-context-mcp                                   
tcp    ESTAB      172.18.0.5:33646                         104.16.11.34:443                         pid=403 npm exec @sammysnake/fast-context-mcp                                   
tcp    ESTAB      172.18.0.5:33650                         104.16.11.34:443                         pid=403 npm exec @sammysnake/fast-context-mcp                                   
tcp    ESTAB      172.18.0.5:33656                         104.16.11.34:443                         pid=403 npm exec @sammysnake/fast-context-mcp                                   
tcp    ESTAB      172.18.0.5:33666                         104.16.11.34:443                         pid=403 npm exec @sammysnake/fast-context-mcp                                   
tcp    ESTAB      172.18.0.5:33668                         104.16.11.34:443                         pid=403 npm exec @sammysnake/fast-context-mcp                                   
tcp    ESTAB      172.18.0.5:33682                         104.16.11.34:443                         pid=403 npm exec @sammysnake/fast-context-mcp                                   
tcp    ESTAB      172.18.0.5:46978                         136.81.151.247:443                       pid=262 /home/user/.amp/bin/amp --dangerously-allow-all --headless=T-01a1cf5c-fc45-713b-98e9-b5d19
```

### 10b. Interfaces / routes
```console
eth0 up c2:48:ac:1d:ef:b0
lo unknown 00:00:00:00:00:00
eth0 ip: 172.18.0.5
Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT                                                       
eth0	00000000	010012AC	0003	0	0	0	00000000	0	0	0                                                                               
eth0	000012AC	00000000	0001	0	0	0	00FFFFFF	0	0	0                                                                               
```

### 12b. Remote peers
```console
ampcode.com -> 34.54.147.251 
t-03h80q0rd6214ksqem1qfl9vp-p20629.onamp.dev -> 34.49.94.208 
34.54.147.251   251.147.54.34.bc.googleusercontent.com
136.68.113.218  218.113.68.136.bc.googleusercontent.com
136.81.151.247  247.151.81.136.bc.googleusercontent.com
--- headless.log (last 15 lines, cut to 240 chars)
{"@timestamp":"2026-11-12T10:48:50.119Z","level":"INFO","message":"[thread-client] Received server message","logger":"","threadId":"T-01a1cf5c-fc45-713b-98e9-b5d19612cc45","type":"inference_tools","direction":"receive","frameMessageCount":1
{"@timestamp":"2026-11-12T10:48:50.310Z","level":"INFO","message":"[thread-client] Received server message","logger":"","threadId":"T-01a1cf5c-fc45-713b-98e9-b5d19612cc45","type":"agent_state","direction":"receive","frameMessageCount":1,"fr
{"@timestamp":"2026-11-12T10:48:50.310Z","level":"INFO","message":"[observer] onAgentState","logger":"","threadId":"T-01a1cf5c-fc45-713b-98e9-b5d19612cc45","type":"agent_state","direction":"receive","messageId":"M-034wGJsxiLeEOliH9K7X4t","s
{"@timestamp":"2026-11-12T10:48:50.310Z","level":"INFO","message":"[thread-client] Received server message","logger":"","threadId":"T-01a1cf5c-fc45-713b-98e9-b5d19612cc45","type":"delta","direction":"receive","frameMessageCount":1,"frameMes
{"@timestamp":"2026-11-12T10:48:56.313Z","level":"INFO","message":"[thread-client] Received server message","logger":"","threadId":"T-01a1cf5c-fc45-713b-98e9-b5d19612cc45","type":"delta","direction":"receive","frameMessageCount":1,"frameMes
{"@timestamp":"2026-11-12T10:48:56.323Z","level":"INFO","message":"[thread-client] Received server message","logger":"","threadId":"T-01a1cf5c-fc45-713b-98e9-b5d19612cc45","type":"delta","direction":"receive","frameMessageCount":1,"frameMes
{"@timestamp":"2026-11-12T10:48:56.323Z","level":"INFO","message":"[thread-client] Received server message","logger":"","threadId":"T-01a1cf5c-fc45-713b-98e9-b5d19612cc45","type":"delta","direction":"receive","frameMessageCount":1,"frameMes
{"@timestamp":"2026-11-12T10:49:00.152Z","level":"INFO","message":"[thread-client] Received server message","logger":"","threadId":"T-01a1cf5c-fc45-713b-98e9-b5d19612cc45","type":"delta","direction":"receive","frameMessageCount":1,"frameMes
{"@timestamp":"2026-11-12T10:49:00.702Z","level":"INFO","message":"[thread-client] Received server message","logger":"","threadId":"T-01a1cf5c-fc45-713b-98e9-b5d19612cc45","type":"message_added","direction":"receive","frameMessageCount":1,"
{"@timestamp":"2026-11-12T10:49:00.703Z","level":"INFO","message":"[observer] onSessionState","logger":"","threadId":"T-01a1cf5c-fc45-713b-98e9-b5d19612cc45","data":{"state":"connected","role":"executor","clientId":"cli-headless-18824ca8-b1
{"@timestamp":"2026-11-12T10:49:00.703Z","level":"INFO","message":"[observer] onMessageAdded","logger":"","threadId":"T-01a1cf5c-fc45-713b-98e9-b5d19612cc45","type":"message_added","direction":"receive","seq":38,"messageId":"M-034wGJsxiLeEO
{"@timestamp":"2026-11-12T10:49:00.895Z","level":"INFO","message":"[thread-client] Received server message","logger":"","threadId":"T-01a1cf5c-fc45-713b-98e9-b5d19612cc45","type":"agent_state","direction":"receive","frameMessageCount":1,"fr
{"@timestamp":"2026-11-12T10:49:00.895Z","level":"INFO","message":"[observer] onAgentState","logger":"","threadId":"T-01a1cf5c-fc45-713b-98e9-b5d19612cc45","type":"agent_state","direction":"receive","messageId":"M-034wGJsxiLeEOliH9K7X4t","s
{"@timestamp":"2026-11-12T10:49:00.898Z","level":"INFO","message":"[thread-client] Received server message","logger":"","threadId":"T-01a1cf5c-fc45-713b-98e9-b5d19612cc45","type":"delta","direction":"receive","frameMessageCount":1,"frameMes
{"@timestamp":"2026-11-12T10:49:01.351Z","level":"INFO","message":"[thread-client] Received server message","logger":"","threadId":"T-01a1cf5c-fc45-713b-98e9-b5d19612cc45","type":"agent_state","direction":"receive","frameMessageCount":1,"fr
--- http-headless-supervisor.sh
#!/usr/bin/env bash
set -uo pipefail

supervisor_pid_file=/home/user/.amp/http-headless-supervisor.pid
headless_pid_file=/home/user/.amp/http-headless.pid
log_file=/home/user/.cache/amp/logs/headless.log
child_pid=''

write_pid() {
	printf '%s
' "$2" >"$1.$$"
	mv -f "$1.$$" "$1"
}
shutdown() {
	trap - TERM INT EXIT
	if [ -n "$child_pid" ]; then
		kill -TERM "$child_pid" 2>/dev/null || true
	fi
	rm -f "$supervisor_pid_file" "$headless_pid_file"
	exit 0
}
trap shutdown TERM INT
trap 'rm -f "$supervisor_pid_file" "$headless_pid_file"' EXIT
mkdir -p "$(dirname "$supervisor_pid_file")" "$(dirname "$log_file")"
write_pid "$supervisor_pid_file" "$$"

while true; do
	rm -f "$headless_pid_file"
	set -a
	. /home/user/.env
	set +a
	/bin/bash /home/user/.amp/amp-headless-update-binary >>"$log_file" 2>&1 || true
	if ! cd "$AMP_WORKING_DIRECTORY" 2>>"$log_file"; then
		echo "amp --headless supervisor: cannot enter working directory $AMP_WORKING_DIRECTORY; not starting amp --headless" >>"$log_file"
		exit 1
	fi
	/home/user/.amp/bin/amp --dangerously-allow-all --headless="$AMP_THREAD_ID" --log-file=/home/user/.cache/amp/logs/headless.log >>"$log_file" 2>&1 &
	child_pid=$!
	write_pid "$headless_pid_file" "$child_pid"
	wait "$child_pid" || true
	child_pid=''
	rm -f "$headless_pid_file"
	sleep 10
done
--- systemctl is-system-running
offline
--- ls /run/amp /opt/amp-wayland
/opt/amp-wayland:
bin
lib
libexec
share

/run/amp:
workload-identity-request-token
```

### 14b. Guidance the agent received about the orb (from its context, not from files in the orb)

- `/home/user/.config/amp/AGENTS.md`: full contents are shown in section 14 above. It says "E2B sandbox", but `AMP_ORB_PROVIDER=orb-provider` and PID 1 is `docker-init`, so it is at least a Docker container from a custom provider. Whether E2B also runs underneath cannot be determined from inside.
- `amp-guidance://user-settings/AGENTS.md`: the user's private global instructions (logging with agent-logs, the 酒狐 persona, git commit format). It is a virtual path, not a file in the orb.
- Orb guidance in the system prompt, summarized: run servers with `amp orb services ensure` or `amp orb service start ... --portal`; use only portal URLs and never raw localhost; Desktop scripts go through `amp-desktop-run:`; the Terminal tab is tmux `-L amp-terminal`, session `amp-terminal-T-01a1cf5c-...`; there is no KVM, so no Android emulators; commit project setup to `.agents/setup`; `amp orb restart-processes` refreshes secrets.
- Skills available: browser-use, building-plugins, building-schedules, building-skills, code-tools:ast-grep, code-tools:merge-conflicts, creating-artifacts, creating-webhooks, deploying-boxes, explaining-code, orb-setup, proving-changes, querying-codegraph, recording-agent-logs, researching-library-docs, searching-code-context, setup-tmux, using-agent-browser, writing-prompts. The official skills sit on the server and are not on disk here. The `find` for `*orb-setup*/SKILL.md` returned nothing.

## Conclusions

- Container: a Debian 12 Docker-based container (PID 1 `docker-init`, overlay rootfs on btrfs, Docker-generated resolv.conf, `AMP_ORB_PROVIDER=orb-provider`). These observations show Docker, but they cannot rule out another layer, such as E2B, underneath it. The kernel is `7.3.0-5-cachyos`.
- Resources: `nproc`/`free` report 24 CPUs / 45 GiB, but the cgroup limits are lower. Raw values:
  - `cpu.max` 1600000 100000 (a quota of about 16 CPUs)
  - `cpuset.cpus.effective` 0-23
  - `memory.max` 34359738368 (32 GiB)
  - `memory.swap.max` 34359738368 (32 GiB)
  - `pids.max` 77037
  - `/proc/self/cgroup` is `0::/`, a cgroup namespace.
- Access: the user is `user`, with passwordless sudo and the docker group. systemd is `offline`, so `amp orb desktop ensure` fails, and there is no DISPLAY or X/Wayland process.
- Redaction check: grepping this report for secret-looking assignments and token patterns (sk-, ghp_, gho_, glpat-, JWT) found 0 matches.
- Agent loop: the orb is only the tool executor. The model loop and inference run remotely. Evidence:
  - `amp --headless=T-...` (pid 262) is the parent of every tool shell.
  - Its log shows `role: "executor"`, `executor_tool_result` sent to the server, and `inference_tools` and `delta` frames received from it.
  - Its outbound connections go to 34.54.147.251:443 (= ampcode.com) and 136.81.151.247:443 (GCP).
  - `amp orb bootstrap` (pid 8) holds connections to ampcode.com and 136.68.113.218 (GCP).
  - No model-provider endpoints were seen.
- Portals: `amp orb service start --portal` started a supervised python server on :31262 and a node portal proxy on 0.0.0.0:20629. It wrote `.amp/portals/demo.json` with `https://t-03h80q0rd6214ksqem1qfl9vp-p20629.onamp.dev/`, which resolves to 34.49.94.208 (Google LB). Curling that URL from inside the orb returned a 302 to ampcode.com portal-auth, so it was not opened directly without signing in.
- Network: eth0 172.18.0.5/24 behind gateway 172.18.0.1. The public egress IP is 64.20.45.163. DNS is 1.1.1.1 and 8.8.8.8. The amp net proxy is off.
- The amp-terminal tmux server did not exist yet, because the Terminal tab had not been opened.
