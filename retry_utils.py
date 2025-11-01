#!/usr/bin/env python3

"""
Retry utilities with exponential backoff for transient failures.

This module provides decorators and functions for implementing retry logic
with exponential backoff and jitter to handle temporary network issues,
API rate limits, and other transient failures.
"""

import asyncio
import logging
import random
from typing import TypeVar, Callable, Any, Optional
from functools import wraps

logger = logging.getLogger(__name__)

T = TypeVar("T")


class RetryExhaustedError(Exception):
    """Raised when all retry attempts have been exhausted"""

    pass


async def retry_with_backoff(
    func: Callable[..., T],
    *args,
    max_attempts: int = 5,
    initial_delay: float = 1.0,
    max_delay: float = 60.0,
    exponential_base: float = 2.0,
    jitter: bool = True,
    exceptions: tuple = (Exception,),
    **kwargs,
) -> T:
    """
    Retry a function with exponential backoff.

    Args:
        func: Function to retry (can be sync or async)
        *args: Positional arguments to pass to func
        max_attempts: Maximum number of retry attempts
        initial_delay: Initial delay between retries in seconds
        max_delay: Maximum delay between retries in seconds
        exponential_base: Base for exponential backoff calculation
        jitter: Whether to add random jitter to delay
        exceptions: Tuple of exceptions to catch and retry on
        **kwargs: Keyword arguments to pass to func

    Returns:
        Result of successful function call

    Raises:
        RetryExhaustedError: If all retry attempts are exhausted
        Exception: The last exception if retries are exhausted
    """
    last_exception = None

    for attempt in range(1, max_attempts + 1):
        try:
            # Call function (handle both sync and async)
            if asyncio.iscoroutinefunction(func):
                result = await func(*args, **kwargs)
            else:
                result = func(*args, **kwargs)

            if attempt > 1:
                logger.info(f"Function {func.__name__} succeeded on attempt {attempt}")

            return result

        except exceptions as e:
            last_exception = e

            if attempt == max_attempts:
                logger.error(
                    f"Function {func.__name__} failed after {max_attempts} attempts: {e}"
                )
                break

            # Calculate delay with exponential backoff
            delay = min(initial_delay * (exponential_base ** (attempt - 1)), max_delay)

            # Add jitter to prevent thundering herd
            if jitter:
                delay = delay * (0.5 + random.random())

            logger.warning(
                f"Function {func.__name__} failed on attempt {attempt}/{max_attempts}: {e}. "
                f"Retrying in {delay:.2f}s..."
            )

            await asyncio.sleep(delay)

    # All retries exhausted
    raise RetryExhaustedError(
        f"Function {func.__name__} failed after {max_attempts} attempts"
    ) from last_exception


def with_retry(
    max_attempts: int = 5,
    initial_delay: float = 1.0,
    max_delay: float = 60.0,
    exponential_base: float = 2.0,
    jitter: bool = True,
    exceptions: tuple = (Exception,),
):
    """
    Decorator to add retry logic with exponential backoff to async functions.

    Example:
        @with_retry(max_attempts=3, initial_delay=2.0)
        async def fetch_data():
            # ... network call that might fail
            pass

    Args:
        max_attempts: Maximum number of retry attempts
        initial_delay: Initial delay between retries in seconds
        max_delay: Maximum delay between retries in seconds
        exponential_base: Base for exponential backoff calculation
        jitter: Whether to add random jitter to delay
        exceptions: Tuple of exceptions to catch and retry on

    Returns:
        Decorated function with retry logic
    """

    def decorator(func: Callable) -> Callable:
        @wraps(func)
        async def wrapper(*args, **kwargs):
            return await retry_with_backoff(
                func,
                *args,
                max_attempts=max_attempts,
                initial_delay=initial_delay,
                max_delay=max_delay,
                exponential_base=exponential_base,
                jitter=jitter,
                exceptions=exceptions,
                **kwargs,
            )

        return wrapper

    return decorator


class RetryableOperation:
    """
    Context manager for retryable operations with statistics tracking.

    Example:
        async with RetryableOperation("fetch_config", max_attempts=3) as op:
            data = await fetch_from_api()
            op.success()
    """

    def __init__(
        self,
        operation_name: str,
        max_attempts: int = 5,
        initial_delay: float = 1.0,
        max_delay: float = 60.0,
        jitter: bool = True,
    ):
        self.operation_name = operation_name
        self.max_attempts = max_attempts
        self.initial_delay = initial_delay
        self.max_delay = max_delay
        self.jitter = jitter
        self.attempt = 0
        self.succeeded = False

    async def __aenter__(self):
        self.attempt += 1
        logger.debug(f"Starting {self.operation_name} (attempt {self.attempt}/{self.max_attempts})")
        return self

    async def __aexit__(self, exc_type, exc_val, exc_tb):
        if exc_type is None:
            self.succeeded = True
            if self.attempt > 1:
                logger.info(
                    f"{self.operation_name} succeeded on attempt {self.attempt}"
                )
            return True

        if self.attempt < self.max_attempts:
            delay = min(
                self.initial_delay * (2 ** (self.attempt - 1)), self.max_delay
            )
            if self.jitter:
                delay = delay * (0.5 + random.random())

            logger.warning(
                f"{self.operation_name} failed (attempt {self.attempt}/{self.max_attempts}): {exc_val}. "
                f"Retrying in {delay:.2f}s..."
            )
            await asyncio.sleep(delay)
            return False  # Suppress exception to allow retry

        logger.error(
            f"{self.operation_name} failed after {self.max_attempts} attempts: {exc_val}"
        )
        return False  # Re-raise the exception

    def success(self):
        """Mark operation as successful"""
        self.succeeded = True

    def retry(self, exception: Exception):
        """Explicitly trigger a retry by raising an exception.

        This method is useful when you want to manually control retry logic
        from within the context manager block.

        Args:
            exception: The exception that triggered the retry

        Raises:
            The provided exception, which will be handled by __aexit__
        """
        raise exception
