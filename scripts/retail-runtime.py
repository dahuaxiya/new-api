"""为已由 release 构建的二进制输出运行时 Dockerfile；不接受线上镜像作为输入。"""
import argparse
import re
from pathlib import Path


def render(recipe: str, revision: str, version: str) -> str:
    """沿用仓库最后一阶段的环境，只将跨阶段复制改为复制发布包中的程序。"""
    if not re.fullmatch(r'[0-9a-f]{40}', revision) or not re.fullmatch(r'[a-z0-9][a-z0-9.-]{0,100}', version):
        raise ValueError('无效的 release 元数据')
    stages = list(re.finditer(r'^FROM\s+', recipe, re.MULTILINE | re.IGNORECASE))
    if not stages:
        raise ValueError('缺少运行时阶段')
    runtime = recipe[stages[-1].start():].strip()
    # 如果上游改变基础镜像、应用路径或增加新的构建阶段依赖，拒绝猜测发布行为。
    if not re.fullmatch(r'FROM debian:[\w.-]+@sha256:[0-9a-f]{64}', runtime.splitlines()[0]):
        raise ValueError('运行时必须使用固定摘要的 Debian 基础镜像')
    src = 'COPY --from=builder2 /build/new-api /'
    if runtime.splitlines().count(src) != 1 or runtime.count('--from=') != 1:
        raise ValueError('运行时阶段与预编译发布契约不符')
    return (runtime.replace(src, 'COPY new-api /new-api') + '\nENV VERSION=' + version
            + '\nLABEL org.opencontainers.image.revision=' + revision
            + '\nLABEL org.opencontainers.image.licenses=AGPL-3.0\n')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('revision'); parser.add_argument('version')
    args = parser.parse_args()
    print(render((Path(__file__).resolve().parents[1] / 'Dockerfile').read_text(), args.revision, args.version), end='')
