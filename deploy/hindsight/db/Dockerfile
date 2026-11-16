# 来自官方配方 docker/docker-compose/pgroonga/Dockerfile（Hindsight v1.2.1）。
# 官方钉的 postgresql-17-pgvector=0.8.6-1.pgdg13+1 已从 PGDG 源下架（2026-11-16 构建失败），改钉 0.8.7-1.pgdg13+2。
FROM groonga/pgroonga:4.0.8-debian-17@sha256:15ab42f1b92749950da640b6d88f5563498a2ac06b2d0aaee1f9185afe34e376
RUN apt-get update && apt-get install -y --no-install-recommends \
    postgresql-17-pgvector=0.8.7-1.pgdg13+2 \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*
