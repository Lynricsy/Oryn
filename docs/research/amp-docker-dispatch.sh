#!/usr/bin/env bash
# Amp 自托管 Orb 的 Docker 调度程序。
# 由 `amp orb provider` 每个任务调用一次，任务参数通过环境变量传入：
#   ORB_OPERATION  spawn | pause | delete
#   ORB_ID         orb 标识，同时用作容器名
#   ORB_IMAGE      spawn 时使用的镜像
#   ORB_SNAPSHOT_ID 项目快照（本机 docker 镜像），可能未设置
#   ORB_TOKEN / AMP_URL  spawn 时传入 orb 内部的 bootstrap
# 契约：每个操作必须可重复执行，且 5 分钟内完成。
set -euo pipefail

# 单个 orb 的资源上限，可在 provider.env 中覆盖
ORB_CPUS="${ORB_CPUS:-16}"
ORB_MEMORY="${ORB_MEMORY:-32g}"

exists() { docker container inspect "$ORB_ID" >/dev/null 2>&1; }

case "$ORB_OPERATION" in
	spawn)
		if exists; then
			docker start "$ORB_ID" >/dev/null
		else
			image="$ORB_IMAGE"
			if [[ -n "${ORB_SNAPSHOT_ID-}" ]] && docker image inspect "$ORB_SNAPSHOT_ID" >/dev/null 2>&1; then
				image="$ORB_SNAPSHOT_ID"
			fi
			# --shm-size：orb 内浏览器需要超过默认 64MB 的共享内存
			docker run --init --platform linux/amd64 -d --name "$ORB_ID" \
				--label amp-orb=1 \
				--cpus "$ORB_CPUS" --memory "$ORB_MEMORY" --shm-size 1g \
				"$image" sleep infinity >/dev/null
		fi
		# 未设置的变量不会传入容器
		docker exec -d --env ORB_TOKEN --env AMP_URL "$ORB_ID" amp orb bootstrap
		echo "spawned $ORB_ID"
		;;
	pause)
		if exists; then
			docker stop "$ORB_ID" >/dev/null
			if [[ -n "${ORB_SNAPSHOT_ID-}" ]]; then
				docker commit "$ORB_ID" "$ORB_SNAPSHOT_ID" >/dev/null
			fi
		fi
		echo "paused $ORB_ID"
		;;
	delete)
		if exists; then
			docker rm -f "$ORB_ID" >/dev/null
		fi
		echo "deleted $ORB_ID"
		;;
	*)
		echo "unknown ORB_OPERATION: ${ORB_OPERATION-}" >&2
		exit 1
		;;
esac
