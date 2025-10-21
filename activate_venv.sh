#!/bin/bash
# Скрипт для активации виртуального окружения

echo "Активация виртуального окружения..."
source venv/bin/activate
echo "Виртуальное окружение активировано!"
echo "Python путь: $(which python)"
echo "Python версия: $(python --version)"
