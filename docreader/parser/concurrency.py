import logging
import threading
from contextlib import contextmanager
from typing import Any, Dict, Iterator

logger = logging.getLogger(__name__)

_LIMITERS: Dict[str, Any] = {}
_LIMITERS_LOCK = threading.Lock()


def use_shared_limiters(limiters: Dict[str, Any]) -> None:
    """在解析子进程中使用父进程创建的跨进程信号量，使 max_workers 限制跨子进程生效。"""
    with _LIMITERS_LOCK:
        _LIMITERS.update(limiters)


def _get_limiter(name: str, max_workers: int) -> Any:
    with _LIMITERS_LOCK:
        limiter = _LIMITERS.get(name)
        if limiter is None:
            limiter = threading.BoundedSemaphore(max_workers)
            _LIMITERS[name] = limiter
        return limiter


@contextmanager
def parser_worker_limit(name: str, max_workers: int) -> Iterator[None]:
    """Limit concurrent access to heavy, process-wide parser operations.

    Set max_workers <= 0 to disable throttling for deployments that have enough
    CPU/GPU resources and know the parser backend is safe under concurrency.
    """

    if max_workers <= 0:
        yield
        return

    limiter = _get_limiter(name, max_workers)
    logger.debug("Waiting for %s parser slot (max_workers=%d)", name, max_workers)
    limiter.acquire()
    try:
        yield
    finally:
        limiter.release()
