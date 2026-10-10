"""在独立子进程中执行文档解析。

单个文件耗尽内存时，内核只会杀掉占用最多的解析子进程，gRPC 主进程和其他请求不受影响。
子进程由 forkserver 创建，避免在多线程的 gRPC 进程中直接 fork。
"""

import logging
import multiprocessing as mp
import threading
import traceback
from typing import Any, Callable, Dict, Optional

from docreader.config import CONFIG
from docreader.models.document import Document

logger = logging.getLogger(__name__)

_CTX = mp.get_context("forkserver")
# 预先在 forkserver 中导入解析模块，避免每个子进程重复导入
_CTX.set_forkserver_preload(["docreader.parser", "docreader.parser.isolation"])

_shared_limiters: Optional[Dict[str, Any]] = None
_shared_limiters_lock = threading.Lock()


class ParseProcessKilled(Exception):
    """解析子进程异常退出，通常是超出内存上限被系统终止。"""


def _get_shared_limiters() -> Dict[str, Any]:
    """跨进程信号量，名称与 parser_worker_limit 的调用处保持一致。"""
    global _shared_limiters
    with _shared_limiters_lock:
        if _shared_limiters is None:
            _shared_limiters = {
                name: _CTX.BoundedSemaphore(max_workers)
                for name, max_workers in (
                    ("markitdown", CONFIG.markitdown_max_workers),
                    ("opendataloader", CONFIG.odl_max_workers),
                )
                if max_workers > 0
            }
        return _shared_limiters


def _child_main(conn, request_id: str, method: str, kwargs: dict, limiters: dict):
    from docreader.parser import Parser
    from docreader.parser.concurrency import use_shared_limiters
    from docreader.utils.request import request_id_context, setup_stdout_logging

    setup_stdout_logging()
    use_shared_limiters(limiters)
    with request_id_context(request_id):
        try:
            conn.send(("ok", getattr(Parser(), method)(**kwargs)))
        except Exception as e:
            logger.error("Error reading document: %s", e)
            logger.info("Traceback: %s", traceback.format_exc())
            conn.send(("error", str(e)))
        finally:
            conn.close()


def parse_isolated(
    method: str,
    request_id: str,
    register_cancel: Callable[[Callable[[], None]], Any],
    **kwargs,
) -> Document:
    """在子进程中调用 Parser().<method>(**kwargs) 并返回结果。

    register_cancel 用于注册“请求结束时杀掉子进程”的回调（传入 gRPC context.add_callback），
    调用方放弃请求（超时、取消）后子进程不会继续占用资源。
    """
    recv_conn, send_conn = _CTX.Pipe(duplex=False)
    proc = _CTX.Process(
        target=_child_main,
        args=(send_conn, request_id, method, kwargs, _get_shared_limiters()),
        name=f"docreader-parse-{request_id}",
    )
    proc.start()
    send_conn.close()
    register_cancel(proc.kill)

    try:
        status, payload = recv_conn.recv()
    except EOFError:
        proc.join()
        raise ParseProcessKilled(
            f"解析进程被系统终止（exitcode={proc.exitcode}），可能超出内存上限"
        )
    finally:
        recv_conn.close()

    proc.join()
    if status == "error":
        raise RuntimeError(payload)
    return payload
