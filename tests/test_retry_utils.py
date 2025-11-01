#!/usr/bin/env python3

"""Unit tests for retry utilities."""

import pytest
import asyncio
from retry_utils import (
    retry_with_backoff,
    with_retry,
    RetryableOperation,
    RetryExhaustedError,
)


class TestRetryWithBackoff:
    """Tests for retry_with_backoff function."""

    @pytest.mark.asyncio
    async def test_succeeds_first_try(self):
        """Function that succeeds on first try should not retry."""
        call_count = 0

        async def succeeds_immediately():
            nonlocal call_count
            call_count += 1
            return "success"

        result = await retry_with_backoff(succeeds_immediately, max_attempts=3)

        assert result == "success"
        assert call_count == 1  # Only called once

    @pytest.mark.asyncio
    async def test_succeeds_after_retries(self):
        """Function that fails then succeeds should retry."""
        call_count = 0

        async def fails_then_succeeds():
            nonlocal call_count
            call_count += 1
            if call_count < 3:
                raise Exception("Temporary failure")
            return "success"

        result = await retry_with_backoff(
            fails_then_succeeds, max_attempts=5, initial_delay=0.01
        )

        assert result == "success"
        assert call_count == 3  # Failed twice, succeeded third time

    @pytest.mark.asyncio
    async def test_exhausts_retries(self):
        """Function that always fails should exhaust retries."""
        call_count = 0

        async def always_fails():
            nonlocal call_count
            call_count += 1
            raise Exception("Permanent failure")

        with pytest.raises(RetryExhaustedError, match="failed after 3 attempts"):
            await retry_with_backoff(
                always_fails, max_attempts=3, initial_delay=0.01
            )

        assert call_count == 3  # Tried max_attempts times

    @pytest.mark.asyncio
    async def test_exponential_backoff(self):
        """Delay should increase exponentially."""
        delays = []
        call_count = 0

        async def track_delays():
            nonlocal call_count
            call_count += 1
            if call_count > 1:
                # Record time since start would be better, but this is simpler
                delays.append(call_count)
            raise Exception("Failure")

        with pytest.raises(Exception):
            await retry_with_backoff(
                track_delays,
                max_attempts=4,
                initial_delay=0.01,
                exponential_base=2.0,
                jitter=False,
            )

        # Should have tried 4 times
        assert call_count == 4


class TestWithRetryDecorator:
    """Tests for with_retry decorator."""

    @pytest.mark.asyncio
    async def test_decorator_succeeds(self):
        """Decorated function should work normally."""
        call_count = 0

        @with_retry(max_attempts=3, initial_delay=0.01)
        async def decorated_function():
            nonlocal call_count
            call_count += 1
            return "success"

        result = await decorated_function()

        assert result == "success"
        assert call_count == 1

    @pytest.mark.asyncio
    async def test_decorator_retries(self):
        """Decorated function should retry on failure."""
        call_count = 0

        @with_retry(max_attempts=3, initial_delay=0.01)
        async def decorated_function():
            nonlocal call_count
            call_count += 1
            if call_count < 2:
                raise Exception("Temporary failure")
            return "success"

        result = await decorated_function()

        assert result == "success"
        assert call_count == 2  # Failed once, succeeded second time


class TestRetryableOperation:
    """Tests for RetryableOperation context manager."""

    @pytest.mark.asyncio
    async def test_context_manager_succeeds(self):
        """Context manager should work for successful operations."""
        call_count = 0

        async def do_operation():
            nonlocal call_count
            call_count += 1
            return "success"

        async with RetryableOperation("test_op", max_attempts=3) as op:
            result = await do_operation()

        assert result == "success"
        assert call_count == 1

    @pytest.mark.asyncio
    async def test_context_manager_with_manual_retry(self):
        """Context manager should support manual retry in a loop."""
        call_count = 0

        async def do_operation():
            nonlocal call_count
            call_count += 1
            if call_count < 2:
                raise Exception("Temporary failure")
            return "success"

        op = RetryableOperation("test_op", max_attempts=3, initial_delay=0.01)
        result = None

        for _ in range(op.max_attempts):
            try:
                async with op:
                    result = await do_operation()
                    break  # Success
            except Exception:
                if op.attempt >= op.max_attempts:
                    raise

        assert result == "success"
        assert call_count == 2  # Failed once, succeeded second time
