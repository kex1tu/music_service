#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
lexicon_data.py — Trigger lexicon and pattern matcher for audio censorship detection.

Categories:
  1. PROFANITY: Russian profanity roots and exact explicit words
  2. DRUG_COAP_6_13: Narcotics, paraphernalia and drug slang (ст. 6.13 КоАП РФ)
  3. ENGLISH_EXPLICIT: English profanity and explicit terms
  4. CENSORSHIP_SUSPECT: Suspect markers and clean euphemisms signaling censorship
"""

import re
from typing import List, Dict, Any, Optional

# 1. Русский мат и грубый сленг
# Используем границы слов на основе набора буквенно-цифровых символов (Unicode-совместимо)
_WORD_BOUNDARY_LEFT = r"(?:^|[^a-zA-Zа-яёА-ЯЁ0-9_])"
_WORD_BOUNDARY_RIGHT = r"(?:$|[^a-zA-Zа-яёА-ЯЁ0-9_])"

PROFANITY_ROOTS_RAW = [
    r"[а-яё]*ху[йеёяию][а-яё]*|нах[уе]й|пох[уе]й",
    r"[а-яё]*п[иеё]зд[а-яё]*",
    r"[а-яё]*[её]б[а-яё]*",
    r"бл[яе]д[а-яё]*|бл[яе]т[а-яё]*",
    r"сук[аеоуи]|сучий|сучка",
]

PROFANITY_EXACT = [
    "целка", "отымел", "гондон", "гандон", "мудак", "мудило",
    "залупа", "шлюха", "дрочить", "дрочит", "отсоси", "соси"
]

# 2. Вещества и нарко-сленг (ст. 6.13 КоАП РФ)
# Основные мишени российской стриминговой цензуры и Роскомнадзора:
DRUG_WORDS = [
    # Каннабиноиды и курение
    "блант", "бланты", "бланта", "блантом",
    "косяк", "косяки", "косяка", "косяком", "косячок",
    "джойнт", "джойнты", "джойнта",
    "сатива", "сативы", "индика", "индики",
    "гашиш", "гаш", "гашика", "гашиком",
    "бошки", "шишки",
    "марихуана", "марихуаны", "марихуану",
    "тгк", "конопля", "конопли",
    "план", "плана", "планом", "планчик",  # Контекст проверяется через is_drug_plan_context()
    "бульбулятор", "бонг", "бонга", "водник", "водника",
    "напас", "напаса", "напасик",
    # Опиаты, аптека, сироп (Lean)
    "кодеин", "кодеина", "лин", "лина", "лином",
    "перкосет", "перкосета", "перкосеты", "перк", "перки",
    "ксанокс", "ксан", "ксанекс", "ксанакс", "ксаны",
    "трамадол", "трамадола", "трамал",
    "прометазин",
    # Тяжелые стимуляторы и эйфоретики
    "мефедрон", "мефедрона", "мефедроном", "меф", "мефа", "мефом",
    "кокаин", "кокаина", "кокаином", "кокс", "кокса", "коксом",
    "амфетамин", "амфетамина", "амф", "спиды", "спидов",
    "экстази", "мдма", "молли", "ешка", "ешки", "ешек",
    "героин", "героина", "герыч", "метадон",
    "мет", "метамфетамин",
    # Сленг распространения и употребления
    "закладка", "закладки", "закладок", "закладку", "кладмен",
    "зиплок", "зиплока", "зиплоки",
    "вмазать", "вмазаться", "вмазался",
    "упороться", "упоролся", "упоротая", "упоротый", "упоротые",
    "передоз", "передоза",
    "стафф", "стаффа"
]

# 3. Английский Explicit (отключен по требованию — английские треки не проверяются на цензуру)
ENGLISH_EXPLICIT = []

# 4. Слова-маркеры подозрения на цензуру (в тексте или тегах)
CENSORSHIP_SUSPECT_MARKERS = [
    "clean", "radio edit", "censored", "tv edit", "clean version",
    "балдеет", "дуреет", "шалеет", "худеет", "блин", "немеет"
]

# Регулярные выражения для выявления житейского/нейтрального контекста слова "план"
# (бизнес-план, план действий, есть план, по плану, строить планы)
NON_DRUG_PLAN_PATTERNS = [
    re.compile(r"(?:есть|был|было|будет|иметь|имею|имеет|имел)\s+(?:[а-яёa-z0-9_-]+\s+)?план", re.IGNORECASE),
    re.compile(r"по\s+плану", re.IGNORECASE),
    re.compile(r"(?:мой|твой|наш|ваш|их|свой|свои|мои|твои|наши)\s+план", re.IGNORECASE),
    re.compile(r"бизнес[- ]план", re.IGNORECASE),
    re.compile(r"план\s+[абbba-zа-я0-9]", re.IGNORECASE),
    re.compile(r"план\s+(?:действий|побега|эвакуации|захвата|капкан|игры|на\s+|урока)", re.IGNORECASE),
    re.compile(r"стро(?:ить|ит|ю|ишь|или|ил)\s+(?:[а-яёa-z0-9_-]+\s+)?план", re.IGNORECASE),
    re.compile(r"состав(?:ить|ил|или|ляю)\s+(?:[а-яёa-z0-9_-]+\s+)?план", re.IGNORECASE),
    re.compile(r"(?:какой|весь|весь этот|весь твой|весь мой)\s+план", re.IGNORECASE),
    re.compile(r"(?:надежный|гениальный|хитрый|четкий|хороший|грамотный)\s+план", re.IGNORECASE),
]


def is_drug_plan_context(word: str, line: str) -> bool:
    """
    Проверяет, употребляется ли слово 'план' в наркотическом значении (ст. 6.13 КоАП),
    а не как обычное житейское слово (план действий, бизнес-план, строить планы).
    """
    w = word.lower().strip()
    if w not in ("план", "плана", "плану", "планом", "планчик", "планчика"):
        return True

    line_lower = line.lower()
    for pat in NON_DRUG_PLAN_PATTERNS:
        if pat.search(line_lower):
            return False

    return True

# Безопасные нецензурные слова и основы, содержащие сочетание "еб" / "ёб"
SAFE_EB_SUBSTRINGS = (
    "хлеб", "погреб", "ребен", "ребят", "колеб", "лебед", "стебел", "стебл", "стеб", "гребл", "греб",
    "дебат", "колыбел", "серебр", "судеб", "треб", "тереб", "враждеб", "служеб",
    "хребет", "хребт", "неб", "череп", "учеб", "себе", "себя", "себой", "тебе", "тебя", "тобой",
    "глеб", "мебел", "мебель", "щебен", "щебень", "щебн", "пренебрег", "пренебреж", "небреж", "жреб",
    "волшеб", "скреб"
)

# Скомпилированные регулярные выражения (хуй, пизд, бляд/т, сука)
PROFANITY_NON_EB_PATTERNS = [
    re.compile(f"{_WORD_BOUNDARY_LEFT}({pat}){_WORD_BOUNDARY_RIGHT}", re.IGNORECASE)
    for pat in [
        r"[а-яё]*ху[йеёяию][а-яё]*|нах[уе]й|пох[уе]й",
        r"[а-яё]*п[иеё]зд[а-яё]*",
        r"бл[яе]д[а-яё]*|бл[яе]т[а-яё]*",
        r"сук[аеоуи]|сучий|сучка",
    ]
]

# Наборы точных совпадений (в нижнем регистре) для O(1) проверки
PROFANITY_EXACT_SET = {w.lower() for w in PROFANITY_EXACT}
DRUG_WORDS_SET = {w.lower() for w in DRUG_WORDS}
ENGLISH_EXPLICIT_SET = {w.lower() for w in ENGLISH_EXPLICIT}
SUSPECT_MARKERS_SET = {w.lower() for w in CENSORSHIP_SUSPECT_MARKERS}

# Регулярка для токенизации слов
TOKEN_RE = re.compile(r"[a-zA-Zа-яёА-ЯЁ0-9]+(?:[-'][a-zA-Zа-яёА-ЯЁ0-9]+)?")


def classify_word(word: str) -> Optional[str]:
    """
    Определяет категорию триггерного слова или возвращает None.
    """
    w = word.lower().strip()
    if not w:
        return None

    # 1. Наркотики ст. 6.13
    if w in DRUG_WORDS_SET:
        return "DRUG_COAP_6_13"

    # 2. Точный мат
    if w in PROFANITY_EXACT_SET:
        return "PROFANITY"

    # 3. Английский explicit
    if w in ENGLISH_EXPLICIT_SET:
        return "ENGLISH_EXPLICIT"

    # 4. Маркеры цензуры / эвфемизмы
    if w in SUSPECT_MARKERS_SET:
        return "SUSPECT_MARKER"

    # 5. Проверка корня 'еб' с исключением безопасных корней
    if ("еб" in w or "ёб" in w) and not any(s in w for s in SAFE_EB_SUBSTRINGS):
        return "PROFANITY"

    # 6. Остальные корни мата через regex (хуй, пизд, бля, сука)
    for pat in PROFANITY_NON_EB_PATTERNS:
        if pat.search(w):
            return "PROFANITY"

    return None


def find_triggers_in_line(line: str) -> List[Dict[str, Any]]:
    """
    Находит все триггерные слова в одной строке текста с их позициями и категориями.
    """
    results = []
    line_clean = line.strip()
    if not line_clean:
        return results

    # 1. Проверка многословных маркеров (например, "radio edit", "clean version")
    line_lower = line_clean.lower()
    for phrase in ["radio edit", "clean version", "tv edit"]:
        idx = line_lower.find(phrase)
        if idx != -1:
            results.append({
                "word": phrase,
                "category": "SUSPECT_MARKER",
                "char_start": idx,
                "char_end": idx + len(phrase),
                "matched_text": line_clean[idx:idx + len(phrase)]
            })

    # 2. Пословный поиск
    for match in TOKEN_RE.finditer(line_clean):
        token = match.group(0)
        cat = classify_word(token)
        if cat == "DRUG_COAP_6_13":
            if not is_drug_plan_context(token, line_clean):
                continue
        if cat:
            results.append({
                "word": token,
                "category": cat,
                "char_start": match.start(),
                "char_end": match.end(),
                "matched_text": token
            })

    # Сортировка по порядку появления в строке и удаление пересекающихся дубликатов
    results.sort(key=lambda x: x["char_start"])
    unique_results = []
    last_end = -1
    for r in results:
        if r["char_start"] >= last_end:
            unique_results.append(r)
            last_end = r["char_end"]

    return unique_results
