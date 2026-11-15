# check=skip=InvalidDefaultArgInFrom
# Oryn Firecracker guest 内核：在 Amazon Linux microvm-kernel 6.1 源码上，以 Firecracker 的 CI 配置为底、
# 追加 kernel.config.fragment 后 olddefconfig 编出 vmlinux（照 AgentLoom 的 kernel-builder.Dockerfile，去掉了 BusyBox / initramfs）。
# 由 scripts/build-fc-kernel.sh 调用；输出 /vmlinux 与最终的 /config（脚本据此核对 fragment 里的选项都生效了）。
ARG ARCH_DIGEST
FROM archlinux@${ARCH_DIGEST} AS builder
ARG ARCH_SNAPSHOT
ARG KERNEL_ARCHIVE_SHA256
ARG KERNEL_CONFIG_SHA256
RUN printf 'Server = https://archive.archlinux.org/repos/%s/$repo/os/$arch\n' "$ARCH_SNAPSHOT" > /etc/pacman.d/mirrorlist \
    && pacman -Syu --noconfirm \
    && pacman -S --noconfirm --needed base-devel bc bison cpio elfutils flex openssl pahole perl tar xz
COPY kernel.tar.gz kernel.config kernel.config.fragment /build/
RUN echo "${KERNEL_ARCHIVE_SHA256}  /build/kernel.tar.gz" | sha256sum -c - \
    && echo "${KERNEL_CONFIG_SHA256}  /build/kernel.config" | sha256sum -c - \
    && mkdir -p /build/kernel /output \
    && tar -xzf /build/kernel.tar.gz --strip-components=1 -C /build/kernel \
    && cp /build/kernel.config /build/kernel/.config \
    && cat /build/kernel.config.fragment >> /build/kernel/.config \
    && make -C /build/kernel olddefconfig \
    && make -C /build/kernel -j"$(nproc)" vmlinux \
    && cp /build/kernel/vmlinux /output/vmlinux \
    && cp /build/kernel/.config /output/config

FROM scratch
COPY --from=builder /output/ /
