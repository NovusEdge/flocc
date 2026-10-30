import os
import stat
from pathlib import Path

LIMIT = 8 * 1024 * 1024
_DIR = os.O_RDONLY | os.O_DIRECTORY


class AgentFileError(OSError):
    pass


def _open_dir(path: Path) -> int:
    """A descriptor for path, reached without following any link flocc-agent could have made."""
    fd = os.open("/", _DIR)
    try:
        for part in Path(os.path.abspath(path)).parts[1:]:
            st = os.stat(part, dir_fd=fd, follow_symlinks=False)
            flags = _DIR | os.O_NOFOLLOW
            if stat.S_ISLNK(st.st_mode):
                # Only root's links are followed (a distro's /home -> var/home); flocc-agent owns
                # everything under its home and could point a link at flocc's secrets.
                if st.st_uid != 0:
                    raise AgentFileError(f"{path}: {part} is a link")
                flags = _DIR
            nfd = os.open(part, flags, dir_fd=fd)
            os.close(fd)
            fd = nfd
    except BaseException:
        os.close(fd)
        raise
    return fd


def _stat(directory: Path, name: str) -> os.stat_result | None:
    try:
        dfd = _open_dir(directory)
    except FileNotFoundError:
        return None
    try:
        return os.stat(name, dir_fd=dfd, follow_symlinks=False)
    except FileNotFoundError:
        return None
    finally:
        os.close(dfd)


def read_text(directory: Path, name: str, limit: int = LIMIT) -> str | None:
    """directory/name as text; None when either is missing. Raises AgentFileError for a link on
    the way, a file that is not regular, or one over limit bytes."""
    try:
        dfd = _open_dir(directory)
    except FileNotFoundError:
        return None
    try:
        # O_NONBLOCK: opening a FIFO flocc-agent left there must not hang the tick.
        fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=dfd)
    except FileNotFoundError:
        return None
    except OSError as exc:
        raise AgentFileError(f"{directory / name}: {exc.strerror}") from exc
    finally:
        os.close(dfd)
    with os.fdopen(fd, "rb") as f:
        if not stat.S_ISREG(os.fstat(f.fileno()).st_mode):
            raise AgentFileError(f"{directory / name} is not a regular file")
        data = f.read(limit + 1)
    if len(data) > limit:
        raise AgentFileError(f"{directory / name} is over {limit} bytes")
    return data.decode(errors="replace")


def exists(directory: Path, name: str) -> bool:
    return _stat(directory, name) is not None


def mtime(directory: Path, name: str) -> float | None:
    st = _stat(directory, name)
    return st.st_mtime if st is not None and stat.S_ISREG(st.st_mode) else None


def entries(directory: Path) -> list[str]:
    """The real subdirectories of directory, by name."""
    try:
        dfd = _open_dir(directory)
    except FileNotFoundError:
        return []
    try:
        names = []
        for name in os.listdir(dfd):
            try:
                st = os.stat(name, dir_fd=dfd, follow_symlinks=False)
            except FileNotFoundError:
                continue
            if stat.S_ISDIR(st.st_mode):
                names.append(name)
        return sorted(names)
    finally:
        os.close(dfd)
