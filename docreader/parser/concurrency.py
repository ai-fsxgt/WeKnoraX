import fcntl
import logging
import os
import tempfile
import time
from contextlib import contextmanager
from typing import Iterator

logger = logging.getLogger(__name__)

# 每个限流名称对应 max_workers 个槽位锁文件。文件锁由操作系统维护，
# 线程与进程之间都互斥，持有者进程被杀时自动释放，不会出现永久占用。
_LOCK_DIR = os.path.join(tempfile.gettempdir(), "docreader-parser-locks")
_RETRY_INTERVAL_SECONDS = 0.2


@contextmanager
def parser_worker_limit(name: str, max_workers: int) -> Iterator[None]:
    """Limit concurrent access to heavy parser operations across threads and processes.

    Set max_workers <= 0 to disable throttling for deployments that have enough
    CPU/GPU resources and know the parser backend is safe under concurrency.
    """

    if max_workers <= 0:
        yield
        return

    os.makedirs(_LOCK_DIR, exist_ok=True)
    logger.debug("Waiting for %s parser slot (max_workers=%d)", name, max_workers)
    while True:
        for slot in range(max_workers):
            lock_file = open(os.path.join(_LOCK_DIR, f"{name}-{slot}.lock"), "w")
            try:
                fcntl.flock(lock_file, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                lock_file.close()
                continue
            try:
                yield
            finally:
                lock_file.close()  # 关闭文件即释放锁
            return
        time.sleep(_RETRY_INTERVAL_SECONDS)
