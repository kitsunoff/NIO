#!/usr/bin/env python3

import kopf
import logging

logger = logging.getLogger(__name__)


def emit_missing_credentials_event(body, reason: str, message: str):
    """Создать событие о недостающих учетных данных"""
    try:
        kopf.warn(body, reason=reason, message=message)
        logger.warning(f"Emitted missing credentials event: {message}")
    except Exception as e:
        logger.error(f"Failed to emit missing credentials event: {e}")


def emit_configuration_applied_event(body, reason: str, message: str):
    """Создать событие о применении конфигурации"""
    try:
        kopf.info(body, reason=reason, message=message)
        logger.info(f"Emitted configuration applied event: {message}")
    except Exception as e:
        logger.error(f"Failed to emit configuration applied event: {e}")


def emit_error_event(body, reason: str, message: str):
    """Создать событие об ошибке"""
    try:
        kopf.exception(body, reason=reason, message=message)
        logger.error(f"Emitted error event: {message}")
    except Exception as e:
        logger.error(f"Failed to emit error event: {e}")
