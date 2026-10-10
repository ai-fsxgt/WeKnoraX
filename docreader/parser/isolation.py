"""在独立子进程中执行文档解析。

单个文件耗尽内存时，内核只会杀掉占用最多的解析子进程，gRPC 主进程和其他请求不受影响。
子进程由 forkserver 创建，避免在多线程的 gRPC 进程中直接 fork。
"""

import logging
import multiprocessing as mp
import os
import signal
import threading
import traceback
from multiprocessing.connection import wait
from typing import Callable

from docreader.models.document import Document

logger = logging.getLogger(__name__)

_CTX = mp.get_context("forkserver")
# 预先在 forkserver 中导入解析模块，避免每个子进程重复导入
_CTX.set_forkserver_preload(["docreader.parser", "docreader.parser.isolation"])


class ParseProcessKilled(Exception):
    """解析子进程异常退出，通常是超出内存上限被系统终止。"""


def _child_main(conn, request_id: str, method: str, kwargs: dict):
    from docreader.parser import Parser
    from docreader.utils.request import request_id_context, setup_stdout_logging

    # 自成进程组，终止时可连同 LibreOffice、ODL、PDF 渲染等孙进程一起清理
    os.setpgrp()
    setup_stdout_logging()
    with request_id_context(request_id):
        try:
            conn.send(("ok", getattr(Parser(), method)(**kwargs)))
        except Exception as e:
            logger.error("Error reading document: %s", e)
            logger.info("Traceback: %s", traceback.format_exc())
            conn.send(("error", str(e)))
        finally:
            conn.close()


def _kill_process_group(pid: int) -> None:
    # killpg 连同孙进程一起终止；子进程刚启动、尚未 setpgrp 时进程组不存在，再单独终止子进程
    for kill in (os.killpg, os.kill):
        try:
            kill(pid, signal.SIGKILL)
        except ProcessLookupError:
            pass  # 进程（组）已退出


def parse_isolated(
    method: str,
    request_id: str,
    register_cancel: Callable[[Callable[[], None]], bool],
    **kwargs,
) -> Document:
    """在子进程中调用 Parser().<method>(**kwargs) 并返回结果。

    register_cancel 用于注册“请求结束时终止子进程”的回调（传入 gRPC context.add_callback），
    返回 False 表示请求已结束；调用方放弃请求（超时、取消）后子进程不会继续占用资源。
    """
    recv_conn, send_conn = _CTX.Pipe(duplex=False)
    proc = _CTX.Process(
        target=_child_main,
        args=(send_conn, request_id, method, kwargs),
        name=f"docreader-parse-{request_id}",
    )
    proc.start()
    send_conn.close()

    # finished 置位后子进程可能已被回收、pid 可能被复用，此后不再发送信号；
    # signal_lock 保证“检查 finished + 发送信号”与“置位 finished”互斥。
    signal_lock = threading.Lock()
    finished = threading.Event()
    cancelled = threading.Event()

    def cancel() -> None:
        with signal_lock:
            if not finished.is_set():
                cancelled.set()
                _kill_process_group(proc.pid)

    def mark_finished() -> None:
        with signal_lock:
            finished.set()

    def reap_descendants() -> None:
        # 解析器用 fork 创建的孙进程会继承管道写端，子进程退出后它们可能让 recv 一直阻塞；
        # 子进程一退出（尚未回收，僵尸保留 pid）就清理整个进程组，关闭残留的写端。
        wait([proc.sentinel])
        with signal_lock:
            if not finished.is_set():
                _kill_process_group(proc.pid)

    if not register_cancel(cancel):
        cancel()
    threading.Thread(target=reap_descendants, daemon=True).start()

    try:
        status, payload = recv_conn.recv()
    except EOFError:
        # 子进程尚未被回收（僵尸保留 pid），清理不持有管道的孙进程（如 LibreOffice）是安全的
        _kill_process_group(proc.pid)
        mark_finished()
        proc.join()
        if cancelled.is_set():
            raise ParseProcessKilled("请求已取消，解析进程已终止")
        raise ParseProcessKilled(
            f"解析进程被系统终止（exitcode={proc.exitcode}），可能超出内存上限"
        )
    finally:
        recv_conn.close()

    mark_finished()
    proc.join()
    if status == "error":
        raise RuntimeError(payload)
    return payload
