/** Evidence-only audit. All state is owned by the shared application store. */
document.addEventListener('alpine:init', () => {
    Alpine.store('app').censorship = {
        runs: [], items: [], providers: ['local'], provider: 'local', path: '', pathsText: '', referenceText: '',
        runId: '', filter: '', offset: 0, busy: false, error: '', poll: null, generation: 0, drafts: {}, audioIssues: {},
        labels: { not_checked: 'Не проверен', insufficient_data: 'Недостаточно данных', suspected: 'Подозрение', checked_words_preserved: 'В проверенных местах слова сохранены' },
        states: { running: 'Проверка', queued: 'В очереди', waiting: 'Ожидание доступа или квоты', paused: 'Приостановлен', cancelled: 'Отменён', completed: 'Завершён', done: 'Обработан', error: 'Ошибка' },
        kinds: { possible_mute: 'Возможная заглушка', possible_beep: 'Возможное запикивание', possible_audio_edit: 'Локальное отличие от эталона', possible_word_change: 'Возможная замена или пропуск слов', words_preserved: 'Строка распознана', unresolved_transcript: 'Расшифровка не дала ответа' },
        async request(path, options) {
            const res = await window.api.raw(`/api/audit/${path}`, options);
            if (!res.ok) { const data = await res.json().catch(() => ({})); throw new Error(data.error || `Ошибка ${res.status}`); }
            return res.json();
        },
        async refresh() {
            const generation = ++this.generation;
            try {
                const data = await this.request('censorship');
                if (generation !== this.generation) return;
                this.error = ""; this.runs = data.runs || []; this.providers = data.providers || ['local'];
                if (!this.runId && this.runs.length) this.runId = this.runs[0].id;
                if (this.runId) {
                    const selected = this.runId;
                    const result = await this.request(`censorship/${encodeURIComponent(selected)}/items?verdict=${encodeURIComponent(this.filter)}&offset=${this.offset}`);
                    if (generation === this.generation && selected === this.runId) {
                        this.items = result.items || [];
                        for (const item of this.items) if (!Object.prototype.hasOwnProperty.call(this.drafts,item.id)) this.drafts[item.id] = item.manual_note || '';
                    }
                }
            } catch (e) { if (generation === this.generation) this.error = e.message; }
        },
        startPolling() {
            this.refresh();
            if (!this.poll) this.poll = setInterval(() => {
                if (Alpine.store('app').activeTab === 'audit' && document.visibilityState === 'visible') this.refresh();
            }, 3000);
        },
        async start() {
            this.busy = true; this.error = '';
            try {
                const references = {};
                for (const line of this.referenceText.split('\n').filter(v => v.trim())) {
                    const parts = line.split(' => ');
                    if (parts.length !== 2) throw new Error('Эталон: путь трека => путь эталона');
                    references[parts[0].trim()] = parts[1].trim();
                }
                await this.startRun({ path: this.path, paths: this.pathsText.split('\n').map(v => v.trim()).filter(Boolean), references });
            } catch (e) { this.error = e.message; } finally { this.busy = false; }
        },
        async startRun(selection) {
            const data = await this.request('censorship', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ ...selection, provider: this.provider }),
            });
            this.runId = data.id;
            this.filter = '';
            this.offset = 0;
            await this.refresh();
        },
        async analyzeCurrent(item) {
            if (this.busy) return;
            this.busy = true;
            this.error = '';
            try { await this.startRun({ paths: [item.path] }); }
            catch (e) { this.error = e.message; }
            finally { this.busy = false; }
        },
        async action(action) {
            if (!this.runId || this.busy) return;
            this.busy = true; this.error = '';
            try { await this.request(`censorship/${encodeURIComponent(this.runId)}/${action}`, { method: 'POST' }); await this.refresh(); }
            catch (e) { this.error = e.message; } finally { this.busy = false; }
        },
        async decide(item, decision) {
            this.error = '';
            try { await this.request(`censorship-items/${item.id}/decision`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ decision, note: this.drafts[item.id] || '' }) }); item.manual_decision = decision; }
            catch (e) { this.error = e.message; }
        },
        async listen(item, evidence, reference = false) {
            const url = `/api/audit/censorship-items/${item.id}/audio?reference=${reference}`;
            const key = reference ? 'reference' : 'target';
            this.audioIssues[item.id] ??= {};
            const issues = this.audioIssues[item.id];
            try {
                const response = await window.api.raw(url, { method: 'HEAD', cache: 'no-store' });
                if (response.status === 409) {
                    issues[key] = reference
                        ? 'Аудио эталона для этого результата недоступно или изменилось.'
                        : 'Проверенная запись недоступна или изменилась. Вердикт и заметка сохранены в этом результате.';
                    return;
                }
                if (!response.ok) throw new Error();
                delete issues[key];
            } catch {
                issues[key] = 'Не удалось проверить доступность аудио. Повторите попытку.';
                return;
            }
            const player = Alpine.store('player');
            player.playCensorshipSpot(url, { start_sec: evidence.start, end_sec: evidence.end, time_sec: evidence.start + 2, time_str: `${Math.floor(evidence.start / 60)}:${String(Math.floor(evidence.start % 60)).padStart(2, '0')}`, word: evidence.expected || this.kinds[evidence.kind] || 'Проверка' }, item.path, reference ? 'Эталон' : 'Проверяемая запись');
        },
        selectRun() { this.offset = 0; this.items = []; this.refresh(); },
        page(delta) { this.offset = Math.max(0, this.offset + delta * 100); this.refresh(); },
    };
});
