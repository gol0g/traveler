// Traveler Dashboard - Main Application Logic

class TravelerApp {
    constructor() {
        this.signals = [];
        this.excluded = new Set();
        this.capital = 50000;
        this.currentSignal = null;
        this.market = 'us'; // 'us' or 'kr'
        this.positionsRefreshTimer = null;
        this.activeTab = 'scanner';

        this.initEventListeners();
        this.initTabs();
        this.initMarketToggle();

        // Auto-load last scan result or attach to running scan
        this.loadLastResult();
    }

    // ==================== TAB NAVIGATION ====================
    initTabs() {
        document.querySelectorAll('.tab-btn').forEach(btn => {
            btn.addEventListener('click', () => this.switchTab(btn.dataset.tab));
        });
    }

    // ==================== MARKET TOGGLE ====================
    initMarketToggle() {
        document.querySelectorAll('.market-btn').forEach(btn => {
            btn.addEventListener('click', () => this.switchMarket(btn.dataset.market));
        });
    }

    switchMarket(market) {
        if (market === this.market) return;
        this.market = market;

        // Update toggle buttons
        document.querySelectorAll('.market-btn').forEach(btn => {
            btn.classList.toggle('active', btn.dataset.market === market);
            if (btn.dataset.market !== market) {
                btn.classList.add('text-gray-400');
            } else {
                btn.classList.remove('text-gray-400');
            }
        });

        // Update capital label and defaults
        const capitalLabel = document.querySelector('label[for="capitalInput"], label');
        const capitalInput = document.getElementById('capitalInput');
        const isSim = market.startsWith('sim-');
        if (market === 'kr' || market === 'sim-kr' || market === 'crypto') {
            if (capitalLabel) capitalLabel.textContent = market === 'crypto' ? 'Capital (₩):' : 'Capital:';
            if (capitalInput && parseFloat(capitalInput.value) < 10000) {
                capitalInput.value = 1000000;
                this.capital = 1000000;
            }
        } else {
            if (capitalLabel) capitalLabel.textContent = 'Capital ($):';
            if (capitalInput && parseFloat(capitalInput.value) >= 10000) {
                capitalInput.value = 200;
                this.capital = 200;
            }
        }

        // sim 마켓에서는 스캔 버튼 비활성화
        const scanBtn = document.getElementById('startScan');
        if (scanBtn) {
            scanBtn.disabled = isSim;
            scanBtn.style.opacity = isSim ? '0.4' : '1';
            scanBtn.title = isSim ? 'Scan runs automatically via daemon' : '';
        }

        // Reload data for active tab
        if (this.activeTab === 'positions') {
            this.loadPositionsData();
        } else if (this.activeTab === 'scanner') {
            this.loadLastResult(true);
        } else if (this.activeTab === 'history') {
            this.loadTradeHistory();
        } else if (this.activeTab === 'dca') {
            this.switchTab('dca'); // re-trigger panel toggle + data load
        }
    }

    isKR() {
        return this.market === 'kr' || this.market === 'sim-kr';
    }

    isCrypto() {
        return this.market === 'crypto';
    }

    // KRW currency (both KR stocks and crypto use Korean Won)
    isKRW() {
        return this.market === 'kr' || this.market === 'sim-kr' || this.market === 'crypto';
    }

    isSim() {
        return this.market === 'sim-us' || this.market === 'sim-kr';
    }

    // Returns market query string (e.g. '?market=kr', '&market=crypto', or '')
    marketQuery(sep = '?') {
        if (this.market === 'us') return '';
        return `${sep}market=${this.market}`;
    }

    switchTab(tab) {
        this.activeTab = tab;

        // Update tab buttons
        document.querySelectorAll('.tab-btn').forEach(btn => {
            btn.classList.toggle('active', btn.dataset.tab === tab);
        });

        // Update panels
        document.getElementById('panelScanner').classList.toggle('hidden', tab !== 'scanner');
        document.getElementById('panelPositions').classList.toggle('hidden', tab !== 'positions');
        document.getElementById('panelHistory').classList.toggle('hidden', tab !== 'history');
        document.getElementById('panelStrategy').classList.toggle('hidden', tab !== 'strategy');
        document.getElementById('panelScalp').classList.toggle('hidden', tab !== 'scalp');
        document.getElementById('panelBinanceScalp').classList.toggle('hidden', tab !== 'binance-scalp');
        document.getElementById('panelBinanceArb').classList.toggle('hidden', tab !== 'binance-arb');
        document.getElementById('panelBtcFutures').classList.toggle('hidden', tab !== 'btc-futures');
        document.getElementById('panelPortfolio').classList.toggle('hidden', tab !== 'portfolio');
        document.getElementById('panelCollector').classList.toggle('hidden', tab !== 'collector');

        // DCA tab: market-aware (crypto → Crypto DCA, kr → KR DCA)
        const isDca = tab === 'dca';
        document.getElementById('panelDca').classList.toggle('hidden', !(isDca && this.market === 'crypto'));
        document.getElementById('panelKrDca').classList.toggle('hidden', !(isDca && this.isKR()));
        document.getElementById('panelDcaNA').classList.toggle('hidden', !(isDca && !this.isCrypto() && !this.isKR()));

        // Load data for specific tabs
        if (tab === 'positions') {
            this.loadPositionsData();
            this.startPositionsRefresh();
        } else {
            this.stopPositionsRefresh();
        }

        if (tab === 'history') {
            this.loadTradeHistory();
        }

        if (tab === 'dca') {
            this.loadDCAForMarket();
        }

        if (tab === 'scalp') {
            this.loadScalpStatus();
        }

        if (tab === 'binance-scalp') {
            this.loadBinanceScalpStatus();
        }

        if (tab === 'binance-arb') {
            this.loadBinanceArbStatus();
        }

        if (tab === 'btc-futures') {
            this.loadBTCFuturesStatus();
            this.initBTCFuturesChartEvents();
            this.loadBTCFuturesChartData();
        }

        if (tab === 'portfolio') {
            this.loadPortfolioOverview();
        }

        if (tab === 'collector') {
            this.loadCollectorStatus();
        }
    }

    startPositionsRefresh() {
        this.stopPositionsRefresh();
        this.positionsRefreshTimer = setInterval(() => {
            this.loadPositionsData();
        }, 30000);
    }

    stopPositionsRefresh() {
        if (this.positionsRefreshTimer) {
            clearInterval(this.positionsRefreshTimer);
            this.positionsRefreshTimer = null;
        }
    }

    // ==================== POSITIONS TAB ====================
    async loadPositionsData() {
        try {
            const mq = this.marketQuery();
            const [posRes, balRes, ordRes] = await Promise.all([
                fetch('/api/positions' + mq).then(r => r.json()).catch(() => ({ positions: [] })),
                fetch('/api/balance' + mq).then(r => r.json()).catch(() => ({})),
                fetch('/api/orders' + mq).then(r => r.json()).catch(() => ({ orders: [] })),
            ]);

            this.renderAccountSummary(balRes, posRes.positions || []);
            this.renderPositionCards(posRes.positions || []);
            this.renderPendingOrders(ordRes.orders || []);

            // Update last refresh time
            const el = document.getElementById('lastRefresh');
            if (el) el.textContent = `(${new Date().toLocaleTimeString()})`;
        } catch (e) {
            console.error('Failed to load positions data:', e);
        }
    }

    renderAccountSummary(balance, positions) {
        const equity = balance.total_equity || 0;
        const cash = balance.cash_balance || 0;

        let totalInvested = 0;
        let totalPnL = 0;
        positions.forEach(p => {
            totalInvested += (p.avg_cost || 0) * (p.quantity || 0);
            totalPnL += p.unrealized_pnl || 0;
        });

        const fmt = (v) => this.formatMoney(v);
        document.getElementById('acctEquity').textContent = fmt(equity);
        document.getElementById('acctCash').textContent = fmt(cash);
        document.getElementById('acctInvested').textContent = fmt(totalInvested);

        const pnlEl = document.getElementById('acctPnL');
        const pnlSign = totalPnL >= 0 ? '+' : '';
        pnlEl.textContent = `${pnlSign}${fmt(totalPnL)}`;
        pnlEl.className = `text-2xl font-bold ${totalPnL > 0 ? 'pnl-positive' : totalPnL < 0 ? 'pnl-negative' : 'pnl-neutral'}`;

        document.getElementById('acctPositionCount').textContent = positions.length;
    }

    renderPositionCards(positions) {
        const container = document.getElementById('positionCards');
        const noPos = document.getElementById('noPositions');

        if (positions.length === 0) {
            container.innerHTML = '';
            noPos.classList.remove('hidden');
            return;
        }

        noPos.classList.add('hidden');
        container.innerHTML = positions.map(pos => this.createPositionCard(pos)).join('');

        // Add click handlers for chart view
        container.querySelectorAll('.position-card').forEach(card => {
            card.addEventListener('click', () => {
                const symbol = card.dataset.symbol;
                this.openPositionChart(symbol, positions.find(p => p.symbol === symbol));
            });
        });
    }

    createPositionCard(pos) {
        const symbol = pos.symbol || '';
        const name = pos.name || '';
        const displayName = name && name !== symbol ? `${symbol} ${name}` : symbol;
        const qty = pos.quantity || 0;
        const avgCost = pos.avg_cost || 0;
        const current = pos.current_price || avgCost;
        const pnl = pos.unrealized_pnl || 0;
        const pnlPct = pos.unrealized_pct || 0;
        const hasPlan = pos.has_plan || false;
        const fp = (v) => this.formatPrice(v);

        // Strategy badge
        const strategy = pos.strategy || '';
        const stratClass = strategy ? `strategy-${strategy}` : 'strategy-unknown';
        const stratLabel = strategy || 'no plan';

        // P&L display
        const pnlSign = pnl >= 0 ? '+' : '';
        const pnlClass = pnl > 0 ? 'pnl-positive' : pnl < 0 ? 'pnl-negative' : 'pnl-neutral';

        // Price level bar
        const priceLevelBar = hasPlan ? this.createPriceLevelBar(pos) : '';

        // Stop/Target info
        const stopTarget = hasPlan ? `
            <div class="grid grid-cols-2 gap-x-4 gap-y-1 text-xs mt-2">
                <div><span class="text-gray-500">Stop:</span> <span class="text-red-400">${fp(pos.stop_loss)}</span> <span class="text-gray-600">(${this.pctDiff(avgCost, pos.stop_loss)})</span></div>
                <div><span class="text-gray-500">T1:</span> <span class="text-green-400">${fp(pos.target1)}</span> <span class="text-gray-600">(${this.pctDiff(avgCost, pos.target1)})</span>${pos.target1_hit ? ' <span class="text-yellow-400">HIT</span>' : ''}</div>
                <div><span class="text-gray-500">T2:</span> <span class="text-green-400">${fp(pos.target2)}</span> <span class="text-gray-600">(${this.pctDiff(avgCost, pos.target2)})</span></div>
                ${pos.breakout_level > 0 ? `<div><span class="text-gray-500">Breakout:</span> <span class="text-orange-400">${fp(pos.breakout_level)}</span></div>` : ''}
            </div>
        ` : '<div class="text-xs text-gray-500 mt-2">No plan data - restart daemon to generate</div>';

        // Time progress
        const timeProgress = hasPlan && pos.max_hold_days > 0 ? this.createTimeProgress(pos) : '';

        // Invalidation warning
        const invalidationWarning = this.createInvalidationWarning(pos);

        return `
            <div class="position-card" data-symbol="${symbol}">
                <div class="flex items-center justify-between mb-2">
                    <div class="flex items-center gap-2">
                        <span class="text-lg font-bold text-white">${displayName}</span>
                        <span class="strategy-badge ${stratClass}">${stratLabel}</span>
                    </div>
                    <div class="text-right">
                        <span class="${pnlClass} font-semibold">${pnlSign}${this.formatMoney(Math.abs(pnl))}</span>
                        <span class="${pnlClass} text-sm ml-1">${pnlSign}${pnlPct.toFixed(1)}%</span>
                    </div>
                </div>
                <div class="text-sm text-gray-400">
                    Entry: ${fp(avgCost)} &nbsp; Current: ${fp(current)} &nbsp; Qty: ${qty}
                </div>
                ${priceLevelBar}
                ${stopTarget}
                ${timeProgress}
                ${invalidationWarning}
            </div>
        `;
    }

    createPriceLevelBar(pos) {
        const stop = pos.stop_loss || 0;
        const entry = pos.avg_cost || 0;
        const current = pos.current_price || entry;
        const t1 = pos.target1 || 0;
        const t2 = pos.target2 || 0;

        if (stop === 0 || t2 === 0) return '';

        const min = stop * 0.995;
        const max = t2 * 1.005;
        const range = max - min;
        if (range <= 0) return '';

        const pct = (val) => Math.max(0, Math.min(100, ((val - min) / range) * 100));

        const stopPct = pct(stop);
        const entryPct = pct(entry);
        const currentPct = pct(current);
        const t1Pct = pct(t1);
        const t2Pct = pct(t2);

        // Fill color based on current position
        let fillColor, fillLeft, fillRight;
        if (current >= entry) {
            fillColor = 'rgba(34, 197, 94, 0.3)';
            fillLeft = entryPct;
            fillRight = currentPct;
        } else {
            fillColor = 'rgba(239, 68, 68, 0.3)';
            fillLeft = currentPct;
            fillRight = entryPct;
        }

        return `
            <div class="price-level-bar">
                <div class="bar-fill" style="left:${fillLeft}%;width:${fillRight - fillLeft}%;background:${fillColor}"></div>
                <div class="bar-marker" style="left:${stopPct}%;background:#ef4444;" title="Stop $${stop.toFixed(2)}"></div>
                <div class="bar-marker" style="left:${entryPct}%;background:#3b82f6;" title="Entry $${entry.toFixed(2)}"></div>
                <div class="bar-marker" style="left:${currentPct}%;background:#ffffff;width:14px;height:14px;" title="Current $${current.toFixed(2)}"></div>
                <div class="bar-marker" style="left:${t1Pct}%;background:#22c55e;" title="T1 $${t1.toFixed(2)}"></div>
                <div class="bar-marker" style="left:${t2Pct}%;background:#22c55e;" title="T2 $${t2.toFixed(2)}"></div>
                <div class="bar-label" style="left:${stopPct}%">SL</div>
                <div class="bar-label" style="left:${entryPct}%">E</div>
                <div class="bar-label" style="left:${currentPct}%;color:#fff;font-weight:600">&#9660;</div>
                <div class="bar-label" style="left:${t1Pct}%">T1</div>
                <div class="bar-label" style="left:${t2Pct}%">T2</div>
            </div>
            <div style="height:16px"></div>
        `;
    }

    createTimeProgress(pos) {
        const held = pos.days_held || 0;
        const max = pos.max_hold_days || 7;
        const remaining = pos.days_remaining || 0;
        const pct = Math.min(100, (held / max) * 100);

        let fillClass = 'time-fill-safe';
        if (pct >= 80) fillClass = 'time-fill-danger';
        else if (pct >= 50) fillClass = 'time-fill-warning';

        return `
            <div class="mt-2">
                <div class="flex justify-between text-xs text-gray-500 mb-1">
                    <span>Day ${held}/${max}</span>
                    <span>${remaining > 0 ? remaining + ' remaining' : 'TIME UP'}</span>
                </div>
                <div class="time-progress">
                    <div class="time-fill ${fillClass}" style="width:${pct}%"></div>
                </div>
            </div>
        `;
    }

    createInvalidationWarning(pos) {
        if (!pos.has_plan) return '';

        // Pullback: consecutive days below MA20
        if (pos.strategy === 'pullback' && pos.consecutive_days_below > 0) {
            return `
                <div class="invalidation-warning">
                    <span>&#9888;</span>
                    <span>Below MA20: ${pos.consecutive_days_below}/2 days</span>
                </div>
            `;
        }

        // Time expired
        if (pos.days_remaining !== undefined && pos.days_remaining <= 0 && pos.max_hold_days > 0) {
            return `
                <div class="invalidation-warning" style="background:rgba(239,68,68,0.15);color:#ef4444">
                    <span>&#9888;</span>
                    <span>Time stop exceeded</span>
                </div>
            `;
        }

        return '';
    }

    renderPendingOrders(orders) {
        const section = document.getElementById('ordersSection');
        const tbody = document.getElementById('ordersTable');

        if (orders.length === 0) {
            section.classList.add('hidden');
            return;
        }

        section.classList.remove('hidden');
        tbody.innerHTML = orders.map(o => `
            <tr class="text-sm">
                <td class="px-4 py-3 font-semibold text-blue-400">${o.symbol}</td>
                <td class="px-4 py-3 ${o.side === 'BUY' ? 'text-green-400' : 'text-red-400'}">${o.side}</td>
                <td class="px-4 py-3">${o.quantity}</td>
                <td class="px-4 py-3">${o.filled_qty}</td>
                <td class="px-4 py-3">${this.formatPrice(o.price || 0)}</td>
                <td class="px-4 py-3"><span class="badge badge-hold">${o.status}</span></td>
            </tr>
        `).join('');
    }

    async openPositionChart(symbol, pos) {
        if (!symbol) return;
        try {
            const res = await fetch(`/api/stock/${symbol}`);
            if (!res.ok) return;
            const data = await res.json();

            const guide = pos && pos.has_plan ? {
                entry_price: pos.avg_cost,
                stop_loss: pos.stop_loss,
                target_1: pos.target1,
                target_2: pos.target2,
            } : null;

            // Reuse stock modal
            const displayLabel = pos && pos.name && pos.name !== symbol ? `${symbol} ${pos.name}` : symbol;
            document.getElementById('modalTitle').textContent = displayLabel;
            document.getElementById('modalEntry').textContent = pos ? this.formatPrice(pos.avg_cost) : '--';
            document.getElementById('modalStopLoss').textContent = pos && pos.stop_loss ? this.formatPrice(pos.stop_loss) : '--';
            document.getElementById('modalTarget1').textContent = pos && pos.target1 ? this.formatPrice(pos.target1) : '--';
            document.getElementById('modalTarget2').textContent = pos && pos.target2 ? this.formatPrice(pos.target2) : '--';
            document.getElementById('modalShares').value = pos ? pos.quantity : 0;
            document.getElementById('modalReason').textContent = pos && pos.strategy ? `Strategy: ${pos.strategy}` : 'No plan';

            if (data.candles && data.candles.length > 0) {
                renderChart('chartContainer', data.candles, guide);
            }

            // Calculate investment/risk for position
            if (pos) {
                const entry = pos.avg_cost || 0;
                const stop = pos.stop_loss || 0;
                const qty = pos.quantity || 0;
                const investment = qty * entry;
                const riskPerShare = stop > 0 ? entry - stop : 0;
                const riskAmount = qty * riskPerShare;
                const riskPct = this.capital > 0 ? (riskAmount / this.capital) * 100 : 0;
                document.getElementById('modalInvestment').textContent = this.formatMoney(investment);
                document.getElementById('modalRiskAmount').textContent = this.formatMoney(riskAmount);
                document.getElementById('modalRiskPct').textContent = `${riskPct.toFixed(2)}%`;
            }

            // Hide scanner-specific buttons
            document.getElementById('excludeBtn').classList.add('hidden');
            document.getElementById('applySharesBtn').classList.add('hidden');

            document.getElementById('stockModal').classList.remove('hidden');
        } catch (e) {
            console.error('Failed to load chart:', e);
        }
    }

    async openScalpChart(symbol, exchange) {
        if (!symbol) return;
        try {
            const endpoint = exchange === 'binance'
                ? `/api/binance-scalp/chart?symbol=${symbol}`
                : `/api/scalp/chart?symbol=${symbol}`;
            const res = await fetch(endpoint);
            if (!res.ok) return;
            const data = await res.json();

            const guide = data.guide ? {
                entry_price: data.guide.entry_price,
                stop_loss: data.guide.stop_loss,
                target_1: data.guide.take_profit,
            } : null;

            const label = exchange === 'binance' ? symbol : symbol.replace('KRW-', '');
            document.getElementById('modalTitle').textContent = `${label} (15m)`;
            document.getElementById('modalEntry').textContent = guide ? guide.entry_price.toFixed(exchange === 'binance' ? 4 : 0) : '--';
            document.getElementById('modalStopLoss').textContent = guide ? guide.stop_loss.toFixed(exchange === 'binance' ? 4 : 0) : '--';
            document.getElementById('modalTarget1').textContent = guide ? guide.target_1.toFixed(exchange === 'binance' ? 4 : 0) : '--';
            document.getElementById('modalTarget2').textContent = '--';
            document.getElementById('modalShares').value = 0;
            document.getElementById('modalReason').textContent = data.guide ? `RSI at entry: ${(data.guide.rsi_at_entry || 0).toFixed(1)}` : 'No position';

            if (data.candles && data.candles.length > 0) {
                renderChart('chartContainer', data.candles, guide);
            }

            document.getElementById('modalInvestment').textContent = '--';
            document.getElementById('modalRiskAmount').textContent = '--';
            document.getElementById('modalRiskPct').textContent = '--';
            document.getElementById('excludeBtn').classList.add('hidden');
            document.getElementById('applySharesBtn').classList.add('hidden');
            document.getElementById('stockModal').classList.remove('hidden');
        } catch (e) {
            console.error('Failed to load scalp chart:', e);
        }
    }

    // ==================== HELPER ====================
    pctDiff(base, target) {
        if (!base || base === 0) return '0%';
        const diff = ((target - base) / base) * 100;
        const sign = diff >= 0 ? '+' : '';
        return `${sign}${diff.toFixed(1)}%`;
    }

    // ==================== SCANNER TAB (existing) ====================
    initEventListeners() {
        // File drop zone
        const dropZone = document.getElementById('dropZone');
        const fileInput = document.getElementById('fileInput');

        dropZone.addEventListener('click', () => fileInput.click());
        dropZone.addEventListener('dragover', (e) => {
            e.preventDefault();
            dropZone.classList.add('dragover');
        });
        dropZone.addEventListener('dragleave', () => {
            dropZone.classList.remove('dragover');
        });
        dropZone.addEventListener('drop', (e) => {
            e.preventDefault();
            dropZone.classList.remove('dragover');
            const files = e.dataTransfer.files;
            if (files.length > 0) {
                this.loadFile(files[0]);
            }
        });
        fileInput.addEventListener('change', (e) => {
            if (e.target.files.length > 0) {
                this.loadFile(e.target.files[0]);
            }
        });

        // Scan button
        document.getElementById('scanBtn').addEventListener('click', () => this.runScan());

        // Load last result button
        document.getElementById('loadLastBtn').addEventListener('click', () => this.loadLastResult());

        // Recalculate button
        document.getElementById('recalculateBtn').addEventListener('click', () => this.recalculate());

        // Capital input
        document.getElementById('capitalInput').addEventListener('change', (e) => {
            this.capital = parseFloat(e.target.value) || 50000;
        });

        // Stock modal
        document.getElementById('closeModal').addEventListener('click', () => this.hideStockModal());
        document.getElementById('excludeBtn').addEventListener('click', () => this.excludeCurrentStock());
        document.getElementById('applySharesBtn').addEventListener('click', () => this.applyShares());
        document.getElementById('modalShares').addEventListener('change', (e) => this.updateModalInvestment(e.target.value));

        // Close modals on escape
        document.addEventListener('keydown', (e) => {
            if (e.key === 'Escape') {
                this.hideStockModal();
            }
        });

        // Click outside modal to close
        document.getElementById('stockModal').addEventListener('click', (e) => {
            if (e.target.id === 'stockModal') this.hideStockModal();
        });
    }

    async loadFile(file) {
        if (!file.name.endsWith('.json')) {
            alert('Please select a JSON file');
            return;
        }

        try {
            const text = await file.text();
            const data = JSON.parse(text);
            this.loadData(data);
        } catch (e) {
            console.error('Failed to parse JSON:', e);
            alert('Failed to parse JSON file: ' + e.message);
        }
    }

    loadData(data) {
        // Support both direct signal array and wrapped format
        if (data.signals) {
            this.signals = data.signals;
            if (data.capital) this.capital = data.capital;
        } else if (Array.isArray(data)) {
            this.signals = data;
        } else if (data.Signals) {
            // Handle Go JSON format (capitalized)
            this.signals = data.Signals;
            if (data.Capital) this.capital = data.Capital;
        }

        // Normalize signal format
        this.signals = this.signals.map(s => this.normalizeSignal(s));

        // Reset excluded
        this.excluded.clear();

        // Update UI
        document.getElementById('capitalInput').value = this.capital;
        this.recalculate();
        this.showUI();
    }

    normalizeSignal(signal) {
        // Handle both camelCase and PascalCase from Go JSON
        return {
            stock: signal.stock || signal.Stock || { symbol: 'Unknown', name: 'Unknown' },
            type: signal.type || signal.Type || 'BUY',
            strategy: signal.strategy || signal.Strategy || 'pullback',
            strength: signal.strength || signal.Strength || 0,
            probability: signal.probability || signal.Probability || 0,
            reason: signal.reason || signal.Reason || '',
            details: signal.details || signal.Details || {},
            guide: signal.guide || signal.Guide || null,
            candles: signal.candles || signal.Candles || [],
            fundamentals: signal.fundamentals || signal.Fundamentals || null,
            ai_reason: signal.ai_reason || '',
            ai_optimize_reason: signal.ai_optimize_reason || ''
        };
    }

    showUI() {
        document.getElementById('dropZone').classList.add('hidden');
        document.getElementById('summaryCards').classList.remove('hidden');
        document.getElementById('controls').classList.remove('hidden');
        document.getElementById('signalsSection').classList.remove('hidden');
    }

    hideUI() {
        document.getElementById('dropZone').classList.remove('hidden');
        document.getElementById('summaryCards').classList.add('hidden');
        document.getElementById('controls').classList.add('hidden');
        document.getElementById('signalsSection').classList.add('hidden');
        document.getElementById('regimeBar').classList.add('hidden');
    }

    recalculate() {
        this.capital = parseFloat(document.getElementById('capitalInput').value) || this.capital;
        const minCap = this.isKRW() ? 100000 : 100;
        if (this.capital < minCap) {
            this.capital = minCap;
            document.getElementById('capitalInput').value = minCap;
        }

        const activeSignals = this.signals.filter(s =>
            !this.excluded.has(s.stock.symbol || s.stock.Symbol)
        );

        // Recalculate position sizing
        let totalInvest = 0;
        let totalRisk = 0;

        if (activeSignals.length > 0) {
            // Match server-side PositionSizer logic:
            // riskBudget = capital * riskPerTrade (NOT divided by positions)
            // maxPositionValue = capital * maxPositionPct
            let riskPct, maxPosPct;
            if (this.isCrypto()) {
                if (this.capital < 500000) {
                    riskPct = 3; maxPosPct = 0.30; // 50만 미만: 공격적
                } else if (this.capital < 5000000) {
                    riskPct = 2; maxPosPct = 0.25; // 500만 미만: 적극적
                } else {
                    riskPct = 1.5; maxPosPct = 0.20; // 500만 이상: 보수적
                }
            } else if (this.isKR()) {
                if (this.capital < 500000) {
                    riskPct = 3; maxPosPct = 0.40; // 50만 미만: 공격적
                } else if (this.capital < 5000000) {
                    riskPct = 2; maxPosPct = 0.30; // 500만 미만: 적극적
                } else {
                    riskPct = 1.5; maxPosPct = 0.25; // 500만 이상: 보수적
                }
            } else {
                if (this.capital < 500) {
                    riskPct = 5; maxPosPct = 0.90; // ETF tier: concentrated
                } else if (this.capital < 5000) {
                    riskPct = 1; maxPosPct = 0.20;
                } else {
                    riskPct = 1; maxPosPct = 0.20;
                }
            }
            const riskBudget = this.capital * (riskPct / 100); // per trade, not per position
            const maxPositionValue = this.capital * maxPosPct;
            const isCrypto = this.isCrypto();

            activeSignals.forEach(signal => {
                if (signal.guide) {
                    const g = signal.guide;
                    const entryPrice = g.entry_price || g.EntryPrice || 0;
                    const stopLoss = g.stop_loss || g.StopLoss || 0;
                    const riskPerShare = entryPrice - stopLoss;

                    if (riskPerShare > 0 && entryPrice > 0) {
                        let sharesByRisk, sharesByAllocation, shares;
                        if (isCrypto) {
                            // Crypto: fractional quantities allowed
                            sharesByRisk = riskBudget / riskPerShare;
                            sharesByAllocation = maxPositionValue / entryPrice;
                            shares = Math.min(sharesByRisk, sharesByAllocation);
                        } else {
                            sharesByRisk = Math.floor(riskBudget / riskPerShare);
                            sharesByAllocation = Math.floor(maxPositionValue / entryPrice);
                            shares = Math.min(sharesByRisk, sharesByAllocation);
                            if (shares < 1) shares = 1; // minimum 1 share
                        }

                        // Cap at maxPositionValue
                        if (shares * entryPrice > maxPositionValue) {
                            shares = isCrypto ? maxPositionValue / entryPrice : Math.floor(maxPositionValue / entryPrice);
                        }
                        if (!isCrypto && shares < 1) shares = 0;

                        // Update guide with new calculations
                        g.position_size = g.PositionSize = shares;
                        g.invest_amount = g.InvestAmount = shares * entryPrice;
                        g.risk_amount = g.RiskAmount = shares * riskPerShare;
                        g.risk_pct = g.RiskPct = (g.risk_amount / this.capital) * 100;
                        g.allocation_pct = g.AllocationPct = (g.invest_amount / this.capital) * 100;

                        totalInvest += g.invest_amount;
                        totalRisk += g.risk_amount;
                    }
                }
            });
        }

        // Update summary cards
        document.getElementById('totalCapital').textContent = this.formatMoney(this.capital);
        document.getElementById('totalInvested').textContent = this.formatMoney(totalInvest);
        document.getElementById('totalRisk').textContent = `${this.formatMoney(totalRisk)} (${(totalRisk / this.capital * 100).toFixed(2)}%)`;
        document.getElementById('cashRemaining').textContent = this.formatMoney(this.capital - totalInvest);

        // Update table
        this.renderTable(activeSignals);
    }

    renderTable(signals) {
        const tbody = document.getElementById('signalsTable');
        tbody.innerHTML = '';

        signals.forEach((signal, index) => {
            const symbol = signal.stock.symbol || signal.stock.Symbol || 'N/A';
            const guide = signal.guide || {};
            const entryPrice = guide.entry_price || guide.EntryPrice || 0;
            const shares = guide.position_size || guide.PositionSize || 0;
            const investAmount = guide.invest_amount || guide.InvestAmount || 0;
            const allocationPct = guide.allocation_pct || guide.AllocationPct || 0;
            const riskAmount = guide.risk_amount || guide.RiskAmount || 0;
            const probability = signal.probability || 0;
            const strategyName = signal.strategy || 'unknown';

            const row = document.createElement('tr');
            row.className = 'hover:bg-gray-750 cursor-pointer';
            const stockName = signal.stock.name || signal.stock.Name || '';
            const displaySym = stockName && stockName !== symbol ? `${symbol} <span class="text-gray-500 text-xs">${stockName}</span>` : symbol;

            const stratColors = {
                'pullback': 'bg-purple-600',
                'breakout': 'bg-orange-600',
                'mean-reversion': 'bg-teal-600',
                'volatility-breakout': 'bg-amber-600',
                'oversold': 'bg-cyan-600',
                'range-trading': 'bg-sky-600',
                'rsi-contrarian': 'bg-violet-600',
                'volume-spike': 'bg-orange-500',
                'etf-momentum': 'bg-blue-600',
                'crypto-trend': 'bg-emerald-600'
            };
            // Parse regime from strategy name like "volatility-breakout(bull)"
            const regimeMatch = strategyName.match(/^(.+)\((bull|sideways|bear)\)$/);
            const baseStrategy = regimeMatch ? regimeMatch[1] : strategyName;
            const regime = regimeMatch ? regimeMatch[2] : (signal.details && signal.details.regime !== undefined ? ({1:'bull',0:'sideways','-1':'bear'}[signal.details.regime] || '') : '');
            const stratBg = stratColors[baseStrategy] || 'bg-gray-600';
            const regimeBadge = regime ? `<span class="${regime === 'bull' ? 'bg-green-700' : regime === 'bear' ? 'bg-red-700' : 'bg-yellow-700'} px-1.5 py-0.5 rounded text-xs ml-1">${regime.toUpperCase()}</span>` : '';

            // Fundamentals summary
            const fund = signal.fundamentals;
            let fundHTML = '<span class="text-gray-500">-</span>';
            if (fund) {
                const isKR = symbol.length === 6 && /^\d+$/.test(symbol);
                const de = fund.debtToEquity || fund.DebtToEquity || 0;
                const pm = ((fund.profitMargins || fund.ProfitMargins || 0) * 100).toFixed(1);
                const w52 = ((fund.fiftyTwoWeekChg || fund.FiftyTwoWeekChg || 0) * 100).toFixed(0);
                const mcap = fund.marketCap || fund.MarketCap || 0;
                const mcapStr = isKR
                    ? (mcap >= 1e12 ? (mcap/1e12).toFixed(1)+'T' : (mcap/1e8).toFixed(0)+'B')
                    : (mcap >= 1e9 ? (mcap/1e9).toFixed(1)+'B' : (mcap/1e6).toFixed(0)+'M');
                fundHTML = `<span class="text-xs">D/E:${de.toFixed(0)} PM:${pm}% 52W:${w52}%</span>`;
            }

            row.innerHTML = `
                <td class="px-4 py-3 text-gray-400">${index + 1}</td>
                <td class="px-4 py-3 font-semibold text-blue-400">${displaySym}</td>
                <td class="px-4 py-3"><span class="${stratBg} px-2 py-0.5 rounded text-xs font-medium">${baseStrategy}</span>${regimeBadge}</td>
                <td class="px-4 py-3">${this.formatPrice(entryPrice)}</td>
                <td class="px-4 py-3">${this.formatQty(shares)}</td>
                <td class="px-4 py-3">${this.formatMoney(investAmount)}</td>
                <td class="px-4 py-3">${allocationPct.toFixed(1)}%</td>
                <td class="px-4 py-3 text-red-400">${this.formatMoney(riskAmount)}</td>
                <td class="px-4 py-3 text-green-400">${probability.toFixed(0)}%</td>
                <td class="px-4 py-3">${fundHTML}</td>
                <td class="px-4 py-3">${signal.ai_reason ? '<span class="bg-purple-600 px-1.5 py-0.5 rounded text-xs" title="' + (signal.ai_reason || '').replace(/"/g, '&quot;') + '">PASS</span>' : '<span class="text-gray-600 text-xs">-</span>'}${signal.ai_optimize_reason ? ' <span class="bg-purple-900 text-purple-300 px-1.5 py-0.5 rounded text-xs" title="' + (signal.ai_optimize_reason || '').replace(/"/g, '&quot;') + '">OPT</span>' : ''}</td>
                <td class="px-4 py-3">
                    <button class="detail-btn bg-gray-700 hover:bg-gray-600 px-3 py-1 rounded text-sm" data-symbol="${symbol}">
                        Detail
                    </button>
                </td>
            `;

            // Click to open modal
            row.querySelector('.detail-btn').addEventListener('click', (e) => {
                e.stopPropagation();
                this.showStockModal(signal);
            });

            tbody.appendChild(row);
        });
    }

    showStockModal(signal) {
        this.currentSignal = signal;
        const modal = document.getElementById('stockModal');
        const symbol = signal.stock.symbol || signal.stock.Symbol || 'N/A';
        const name = signal.stock.name || signal.stock.Name || symbol;
        const guide = signal.guide || {};

        document.getElementById('modalTitle').textContent = `${symbol} - ${name}`;
        document.getElementById('modalEntry').textContent = this.formatPrice(guide.entry_price || guide.EntryPrice || 0);
        document.getElementById('modalStopLoss').textContent = `${this.formatPrice(guide.stop_loss || guide.StopLoss || 0)} (${(guide.stop_loss_pct || guide.StopLossPct || 0).toFixed(1)}%)`;
        document.getElementById('modalTarget1').textContent = `${this.formatPrice(guide.target_1 || guide.Target1 || 0)} (+${(guide.target_1_pct || guide.Target1Pct || 0).toFixed(1)}%)`;
        document.getElementById('modalTarget2').textContent = `${this.formatPrice(guide.target_2 || guide.Target2 || 0)} (+${(guide.target_2_pct || guide.Target2Pct || 0).toFixed(1)}%)`;
        document.getElementById('modalShares').value = guide.position_size || guide.PositionSize || 0;
        document.getElementById('modalReason').textContent = signal.reason || 'N/A';

        // Fundamentals section
        const fund = signal.fundamentals;
        const fundSection = document.getElementById('modalFundamentals');
        if (fund) {
            fundSection.classList.remove('hidden');
            const isKR = (symbol.length === 6 && /^\d+$/.test(symbol));
            const mcap = fund.marketCap || fund.MarketCap || 0;
            const mcapStr = isKR
                ? (mcap >= 1e12 ? `₩${(mcap/1e12).toFixed(1)}T` : `₩${(mcap/1e8).toFixed(0)}B`)
                : (mcap >= 1e9 ? `$${(mcap/1e9).toFixed(1)}B` : `$${(mcap/1e6).toFixed(0)}M`);
            const tpe = fund.trailingPE || fund.TrailingPE || 0;
            const fpe = fund.forwardPE || fund.ForwardPE || 0;
            const de = fund.debtToEquity || fund.DebtToEquity || 0;
            const pm = (fund.profitMargins || fund.ProfitMargins || 0) * 100;
            const w52 = (fund.fiftyTwoWeekChg || fund.FiftyTwoWeekChg || 0) * 100;
            const revg = (fund.revenueGrowth || fund.RevenueGrowth || 0) * 100;
            const roe = (fund.returnOnEquity || fund.ReturnOnEquity || 0) * 100;

            document.getElementById('fundMarketCap').textContent = mcapStr;
            document.getElementById('fundTrailingPE').textContent = tpe > 0 ? tpe.toFixed(1) : 'N/A';
            document.getElementById('fundForwardPE').textContent = fpe > 0 ? fpe.toFixed(1) : 'N/A';

            const deEl = document.getElementById('fundDebtEquity');
            deEl.textContent = `${de.toFixed(1)}%`;
            deEl.className = de > 200 ? 'font-semibold text-red-400' : 'font-semibold';

            const pmEl = document.getElementById('fundProfitMargin');
            pmEl.textContent = `${pm.toFixed(1)}%`;
            pmEl.className = pm < -10 ? 'font-semibold text-red-400' : pm > 10 ? 'font-semibold text-green-400' : 'font-semibold';

            const w52El = document.getElementById('fund52WChange');
            w52El.textContent = `${w52 >= 0 ? '+' : ''}${w52.toFixed(1)}%`;
            w52El.className = w52 < -30 ? 'font-semibold text-red-400' : w52 > 0 ? 'font-semibold text-green-400' : 'font-semibold text-yellow-400';

            document.getElementById('fundRevGrowth').textContent = `${revg >= 0 ? '+' : ''}${revg.toFixed(1)}%`;
            document.getElementById('fundROE').textContent = `${roe.toFixed(1)}%`;
        } else {
            fundSection.classList.add('hidden');
        }

        // AI Analysis section
        const aiSection = document.getElementById('modalAI');
        const aiFilterDiv = document.getElementById('modalAIFilter');
        const aiOptDiv = document.getElementById('modalAIOptimize');
        const aiReason = signal.ai_reason || '';
        const aiOptReason = signal.ai_optimize_reason || '';
        if (aiReason || aiOptReason) {
            aiSection.classList.remove('hidden');
            if (aiReason) {
                aiFilterDiv.classList.remove('hidden');
                document.getElementById('modalAIFilterText').textContent = aiReason;
            } else {
                aiFilterDiv.classList.add('hidden');
            }
            if (aiOptReason) {
                aiOptDiv.classList.remove('hidden');
                document.getElementById('modalAIOptimizeText').textContent = aiOptReason;
            } else {
                aiOptDiv.classList.add('hidden');
            }
        } else {
            aiSection.classList.add('hidden');
        }

        this.updateModalInvestment(guide.position_size || guide.PositionSize || 0);

        // Show scanner-specific buttons
        document.getElementById('excludeBtn').classList.remove('hidden');
        document.getElementById('applySharesBtn').classList.remove('hidden');

        // Render chart
        const candles = signal.candles || [];
        if (candles.length > 0) {
            renderChart('chartContainer', candles, guide);
        }

        modal.classList.remove('hidden');
    }

    hideStockModal() {
        document.getElementById('stockModal').classList.add('hidden');
        this.currentSignal = null;
    }

    updateModalInvestment(shares) {
        if (!this.currentSignal || !this.currentSignal.guide) return;

        const guide = this.currentSignal.guide;
        const entryPrice = guide.entry_price || guide.EntryPrice || 0;
        const stopLoss = guide.stop_loss || guide.StopLoss || 0;
        const riskPerShare = entryPrice - stopLoss;

        const investment = shares * entryPrice;
        const riskAmount = shares * riskPerShare;
        const riskPct = (riskAmount / this.capital) * 100;

        document.getElementById('modalInvestment').textContent = this.formatMoney(investment);
        document.getElementById('modalRiskAmount').textContent = this.formatMoney(riskAmount);
        document.getElementById('modalRiskPct').textContent = `${riskPct.toFixed(2)}%`;
    }

    excludeCurrentStock() {
        if (!this.currentSignal) return;

        const symbol = this.currentSignal.stock.symbol || this.currentSignal.stock.Symbol;
        this.excluded.add(symbol);
        this.hideStockModal();
        this.recalculate();
    }

    applyShares() {
        if (!this.currentSignal || !this.currentSignal.guide) return;

        const shares = parseInt(document.getElementById('modalShares').value) || 0;
        if (shares < 1) {
            alert('Shares must be at least 1');
            return;
        }

        const guide = this.currentSignal.guide;
        const entryPrice = guide.entry_price || guide.EntryPrice || 0;
        const stopLoss = guide.stop_loss || guide.StopLoss || 0;
        const riskPerShare = entryPrice - stopLoss;

        guide.position_size = guide.PositionSize = shares;
        guide.invest_amount = guide.InvestAmount = shares * entryPrice;
        guide.risk_amount = guide.RiskAmount = shares * riskPerShare;
        guide.risk_pct = guide.RiskPct = (guide.risk_amount / this.capital) * 100;
        guide.allocation_pct = guide.AllocationPct = (guide.invest_amount / this.capital) * 100;

        this.hideStockModal();
        this.recalculate();
    }

    async runScan() {
        const capital = parseFloat(document.getElementById('capitalInput').value) || 200;
        const minCapital = this.isKRW() ? 100000 : 100;
        const currLabel = this.isKRW() ? '₩' : '$';
        if (capital < minCapital) {
            alert(`Minimum capital is ${currLabel}${minCapital.toLocaleString()}`);
            return;
        }

        const scanLabel = this.isCrypto() ? 'Crypto Volatility Breakout Scan'
            : this.isKR() ? 'KR Adaptive Multi-Strategy Scan' : 'Adaptive Multi-Strategy Scan';
        this.showLoading(true, scanLabel, 'Starting...');

        try {
            // Fire-and-forget: start scan
            const mq = this.marketQuery('&');
            const startRes = await fetch(`/api/scan?capital=${capital}${mq}`, { method: 'POST' });
            const startData = await startRes.json();

            if (startData.status === 'already_running') {
                // Scan already in progress, just start polling
            } else if (startData.status !== 'started') {
                this.showLoading(false);
                alert('Failed to start scan: ' + JSON.stringify(startData));
                return;
            }

            // Poll for progress
            const statusMq = this.marketQuery();
            this._scanPoll = setInterval(async () => {
                try {
                    const res = await fetch('/api/scan/status' + statusMq);
                    const st = await res.json();

                    const title = document.getElementById('loadingTitle');
                    const detail = document.getElementById('loadingDetail');

                    if (st.status === 'running') {
                        if (title) title.textContent = `Scanned ${st.scanned} | Found ${st.found} signals`;
                        if (detail) detail.textContent = st.message || '';
                    } else if (st.status === 'done') {
                        this.stopScanPoll();
                        if (detail) detail.textContent = 'Loading results...';
                        await this.fetchScanResult(capital);
                    } else if (st.status === 'error') {
                        this.stopScanPoll();
                        this.showLoading(false);
                        alert('Scan failed: ' + (st.error || 'Unknown error'));
                    }
                } catch (err) {
                    // Network blip — keep polling, don't abort
                    console.warn('Poll failed, retrying...', err);
                }
            }, 2000);

        } catch (err) {
            this.showLoading(false);
            alert('Failed to start scan: ' + err.message);
        }
    }

    stopScanPoll() {
        if (this._scanPoll) {
            clearInterval(this._scanPoll);
            this._scanPoll = null;
        }
    }

    async loadLastResult(noAutoSwitch = false) {
        try {
            const mq = this.marketQuery();
            let st = await fetch('/api/scan/status' + mq).then(r => r.json());

            // On initial page load only: if current market has no result, auto-switch
            if (!noAutoSwitch && st.status === 'idle') {
                const otherMarkets = ['us', 'kr', 'crypto'].filter(m => m !== this.market);
                for (const otherMarket of otherMarkets) {
                    const otherMq = otherMarket === 'us' ? '' : `?market=${otherMarket}`;
                    const otherSt = await fetch('/api/scan/status' + otherMq).then(r => r.json());
                    if (otherSt.status === 'done') {
                        this.market = otherMarket;
                        document.querySelectorAll('.market-btn').forEach(btn => {
                            btn.classList.toggle('active', btn.dataset.market === otherMarket);
                            if (btn.dataset.market !== otherMarket) {
                                btn.classList.add('text-gray-400');
                            } else {
                                btn.classList.remove('text-gray-400');
                            }
                        });
                        st = otherSt;
                        break;
                    }
                }
            }

            if (st.status === 'done') {
                const capital = parseFloat(document.getElementById('capitalInput').value) || this.capital;
                await this.fetchScanResult(capital);
            } else if (st.status === 'running') {
                this.showLoading(true, 'Scan in progress', st.message || '');
                const pollMq = this.marketQuery();
                this._scanPoll = setInterval(async () => {
                    try {
                        const poll = await fetch('/api/scan/status' + pollMq).then(r => r.json());
                        const title = document.getElementById('loadingTitle');
                        const detail = document.getElementById('loadingDetail');
                        if (poll.status === 'running') {
                            if (title) title.textContent = `Scanned ${poll.scanned} | Found ${poll.found} signals`;
                            if (detail) detail.textContent = poll.message || '';
                        } else if (poll.status === 'done') {
                            this.stopScanPoll();
                            await this.fetchScanResult(this.capital);
                        } else if (poll.status === 'error') {
                            this.stopScanPoll();
                            this.showLoading(false);
                        }
                    } catch (err) { /* retry */ }
                }, 2000);
            } else {
                const marketLabel = this.isCrypto() ? 'Crypto' : this.isKR() ? 'KR' : 'US';
                document.getElementById('scanMeta').textContent = `No ${marketLabel} scan result — click Scan to start`;
                // Hide results but keep controls (scan button) visible
                document.getElementById('summaryCards').classList.add('hidden');
                document.getElementById('signalsSection').classList.add('hidden');
                document.getElementById('controls').classList.remove('hidden');
            }
        } catch (err) {
            // server not reachable, ignore on auto-load
        }
    }

    async fetchScanResult(capital) {
        try {
            // Fetch result for current market
            const mq = this.marketQuery();
            let res = await fetch('/api/scan/result' + mq);
            if (!res.ok) {
                // No result for this market — clear UI
                this.showLoading(false);
                this.signals = [];
                this.excluded.clear();
                this.recalculate();
                this.hideUI();
                const label = this.isCrypto() ? 'Crypto' : this.isKR() ? 'KR' : 'US';
                document.getElementById('scanMeta').textContent = `No ${label} scan result — click Scan to start`;
                return;
            }
            const data = await res.json();

            this.showLoading(false);

            // Use server capital if available, else input value
            this.capital = data.capital || capital;
            document.getElementById('capitalInput').value = this.capital;

            this.signals = (data.signals || []).map(s => this.normalizeSignal(s));
            this.excluded.clear();
            this.recalculate();
            this.showUI();

            const meta = [];
            if (data.universes_used && data.universes_used.length > 0) {
                meta.push(data.universes_used.join(' → '));
            }
            meta.push(`${data.total_scanned || 0} scanned`);
            if (data.avg_prob > 0) meta.push(`avg ${data.avg_prob.toFixed(0)}% prob`);
            if (data.expansions > 0) meta.push(`${data.expansions}x expanded`);
            if (data.fundamentals_filtered > 0) meta.push(`${data.fundamentals_filtered} rejected by fundamentals`);
            if (data.ai_filtered > 0) meta.push(`🤖 ${data.ai_filtered} rejected by AI`);
            meta.push(data.scan_time || '');
            document.getElementById('scanMeta').textContent = meta.filter(Boolean).join(' | ');
            document.getElementById('recalculateBtn').classList.remove('hidden');

            // AI Rejections list
            const rejSection = document.getElementById('aiRejectionsSection');
            const rejList = document.getElementById('aiRejectionsList');
            if (data.ai_rejections && data.ai_rejections.length > 0) {
                rejSection.classList.remove('hidden');
                rejList.innerHTML = data.ai_rejections.map(r =>
                    `<div class="flex items-center gap-2 text-sm">
                        <span class="text-red-400 font-medium w-24">${r.symbol}</span>
                        <span class="text-gray-400">${r.reason}</span>
                    </div>`
                ).join('');
            } else {
                rejSection.classList.add('hidden');
            }

            // Regime display
            this.updateRegimeBar(data);
        } catch (err) {
            this.showLoading(false);
            alert('Failed to load scan result: ' + err.message);
        }
    }

    updateRegimeBar(data) {
        const bar = document.getElementById('regimeBar');
        if (!data.regime) {
            bar.classList.add('hidden');
            return;
        }
        bar.classList.remove('hidden');

        const badge = document.getElementById('regimeBadge');
        const regime = data.regime.toUpperCase();
        badge.textContent = regime;
        badge.className = 'px-3 py-1 rounded-full text-sm font-bold ';
        if (data.regime === 'bull') {
            badge.className += 'bg-green-900 text-green-300 border border-green-700';
        } else if (data.regime === 'bear') {
            badge.className += 'bg-red-900 text-red-300 border border-red-700';
        } else {
            badge.className += 'bg-yellow-900 text-yellow-300 border border-yellow-700';
        }

        // Benchmark info
        const bench = document.getElementById('regimeBenchmark');
        if (data.benchmark_price > 0) {
            const fmt = this.isKR() ? '₩' + Math.round(data.benchmark_price).toLocaleString()
                      : this.isCrypto() ? '₩' + Math.round(data.benchmark_price).toLocaleString()
                      : '$' + data.benchmark_price.toFixed(2);
            const ma20 = this.isKR() || this.isCrypto()
                ? Math.round(data.benchmark_ma20).toLocaleString()
                : data.benchmark_ma20.toFixed(2);
            const ma50 = this.isKR() || this.isCrypto()
                ? Math.round(data.benchmark_ma50).toLocaleString()
                : data.benchmark_ma50.toFixed(2);
            const priceVsMa20 = data.benchmark_price > data.benchmark_ma20 ? '>' : '<';
            const priceVsMa50 = data.benchmark_price > data.benchmark_ma50 ? '>' : '<';
            bench.innerHTML = `Price ${fmt} ${priceVsMa20} MA20 ${ma20} | ${priceVsMa50} MA50 ${ma50} | RSI ${data.benchmark_rsi.toFixed(1)}`;
        } else {
            bench.textContent = '';
        }

        // Active strategies
        const strats = document.getElementById('regimeStrategies');
        const tierLabel = data.capital_tier ? ` [${data.capital_tier.toUpperCase()} tier]` : '';
        strats.textContent = (data.active_strategies || []).join(', ') + tierLabel;
    }

    showLoading(show, title, detail) {
        const el = document.getElementById('loading');
        el.classList.toggle('hidden', !show);

        if (show) {
            document.getElementById('loadingTitle').textContent = title || 'Scanning stocks...';
            document.getElementById('loadingDetail').textContent = detail || '';
            const timerEl = document.getElementById('loadingTimer');
            const start = Date.now();
            timerEl.textContent = '0s elapsed';
            this._loadingTimer = setInterval(() => {
                const sec = Math.floor((Date.now() - start) / 1000);
                const min = Math.floor(sec / 60);
                timerEl.textContent = min > 0 ? `${min}m ${sec % 60}s elapsed` : `${sec}s elapsed`;
            }, 1000);
        } else {
            if (this._loadingTimer) {
                clearInterval(this._loadingTimer);
                this._loadingTimer = null;
            }
        }
    }

    // ==================== HISTORY TAB ====================
    async loadTradeHistory() {
        try {
            const mq = this.marketQuery();
            const res = await fetch('/api/trade-history' + mq);
            const data = await res.json();

            this.renderHistorySummary(data.summary || {});
            this.renderStrategyPerformance(data.summary || {});
            this.renderHistoryTable(data.records || []);
        } catch (e) {
            console.error('Failed to load trade history:', e);
        }
    }

    renderHistorySummary(summary) {
        const fmt = (v) => this.formatMoney(v);
        const total = summary.total_trades || 0;
        const buys = summary.buy_count || 0;
        const sells = summary.sell_count || 0;
        const wins = summary.win_count || 0;
        const losses = summary.loss_count || 0;
        const winRate = summary.win_rate || 0;
        const pnl = summary.total_realized_pnl || 0;
        const commission = summary.total_commission || 0;
        // Net PnL은 서버에서 직접 계산 (실현 거래 수수료만 반영)
        const netPnl = summary.net_pnl || 0;

        document.getElementById('histTotalTrades').textContent = total;
        document.getElementById('histBuys').textContent = buys;
        document.getElementById('histSells').textContent = sells;
        document.getElementById('histWinRate').textContent = sells > 0 ? `${winRate.toFixed(1)}%` : '--%';
        document.getElementById('histWins').textContent = wins;
        document.getElementById('histLosses').textContent = losses;

        const pnlEl = document.getElementById('histPnL');
        pnlEl.textContent = (pnl >= 0 ? '+' : '') + fmt(pnl);
        pnlEl.className = `text-2xl font-bold ${pnl > 0 ? 'pnl-positive' : pnl < 0 ? 'pnl-negative' : 'pnl-neutral'}`;

        document.getElementById('histCommission').textContent = fmt(commission);

        const netEl = document.getElementById('histNetPnL');
        netEl.textContent = (netPnl >= 0 ? '+' : '') + fmt(netPnl);
        netEl.className = `text-2xl font-bold ${netPnl > 0 ? 'pnl-positive' : netPnl < 0 ? 'pnl-negative' : 'pnl-neutral'}`;
    }

    renderStrategyPerformance(summary) {
        const container = document.getElementById('strategyPerf');
        const byStrategy = summary.by_strategy || {};

        if (Object.keys(byStrategy).length === 0) {
            container.innerHTML = '';
            return;
        }

        const fmt = (v) => this.formatMoney(v);
        container.innerHTML = Object.entries(byStrategy).map(([name, s]) => {
            const stratClass = `strategy-${name}`;
            const pnlClass = s.pnl > 0 ? 'pnl-positive' : s.pnl < 0 ? 'pnl-negative' : 'pnl-neutral';
            const netClass = s.net_pnl > 0 ? 'pnl-positive' : s.net_pnl < 0 ? 'pnl-negative' : 'pnl-neutral';

            return `
                <div class="bg-gray-800 rounded-xl p-4 border border-gray-700">
                    <div class="flex items-center gap-2 mb-3">
                        <span class="strategy-badge ${stratClass}">${name}</span>
                    </div>
                    <div class="grid grid-cols-2 gap-2 text-sm">
                        <div><span class="text-gray-500">Trades:</span> ${s.trades}</div>
                        <div><span class="text-gray-500">Win Rate:</span> ${s.trades > 0 ? s.win_rate.toFixed(1) : 0}%</div>
                        <div><span class="text-gray-500">Wins:</span> <span class="text-green-400">${s.wins}</span></div>
                        <div><span class="text-gray-500">Losses:</span> <span class="text-red-400">${s.losses}</span></div>
                        <div><span class="text-gray-500">P&L:</span> <span class="${pnlClass}">${fmt(s.pnl)}</span></div>
                        <div><span class="text-gray-500">Net:</span> <span class="${netClass}">${fmt(s.net_pnl)}</span></div>
                    </div>
                </div>
            `;
        }).join('');
    }

    renderHistoryTable(records) {
        const tbody = document.getElementById('historyTable');
        const noRecords = document.getElementById('histNoRecords');
        const countEl = document.getElementById('histRecordCount');

        if (records.length === 0) {
            tbody.innerHTML = '';
            noRecords.classList.remove('hidden');
            countEl.textContent = '';
            return;
        }

        noRecords.classList.add('hidden');
        countEl.textContent = `${records.length} records`;

        // Reverse to show newest first
        const sorted = [...records].reverse();

        tbody.innerHTML = sorted.map(r => {
            const date = r.timestamp ? new Date(r.timestamp).toLocaleDateString('ko-KR', {month:'2-digit', day:'2-digit', hour:'2-digit', minute:'2-digit'}) : '--';
            const isSell = r.side === 'sell';
            const sideClass = isSell ? 'text-red-400' : 'text-green-400';
            const sideLabel = isSell ? 'SELL' : 'BUY';

            let pnlHtml = '';
            if (isSell && r.pnl !== undefined) {
                const pnlClass = r.pnl > 0 ? 'pnl-positive' : r.pnl < 0 ? 'pnl-negative' : 'pnl-neutral';
                const sign = r.pnl >= 0 ? '+' : '';
                pnlHtml = `<span class="${pnlClass}">${sign}${this.formatMoney(r.pnl)}</span>`;
                if (r.pnl_pct) {
                    pnlHtml += ` <span class="${pnlClass} text-xs">(${sign}${r.pnl_pct.toFixed(1)}%)</span>`;
                }
            }

            const stratClass = r.strategy ? `strategy-${r.strategy}` : '';
            const stratLabel = r.strategy || '';

            const reasonBadge = r.reason ? this.getReasonBadge(r.reason) : '';

            return `
                <tr class="text-sm">
                    <td class="px-4 py-3 text-gray-400 whitespace-nowrap">${date}</td>
                    <td class="px-4 py-3 font-semibold text-blue-400">${r.symbol}${r.name ? `<span class="text-gray-400 font-normal text-xs ml-1">${r.name}</span>` : ''}</td>
                    <td class="px-4 py-3 ${sideClass} font-medium">${sideLabel}</td>
                    <td class="px-4 py-3">${stratLabel ? `<span class="strategy-badge ${stratClass} text-xs">${stratLabel}</span>` : ''}</td>
                    <td class="px-4 py-3">${r.quantity}</td>
                    <td class="px-4 py-3">${this.formatPrice(r.price || 0)}</td>
                    <td class="px-4 py-3">${this.formatMoney(r.amount || 0)}</td>
                    <td class="px-4 py-3">${pnlHtml}</td>
                    <td class="px-4 py-3">${reasonBadge}</td>
                </tr>
            `;
        }).join('');
    }

    getReasonBadge(reason) {
        const reasonColors = {
            'signal': 'bg-blue-600',
            'stop_loss': 'bg-red-600',
            'target1': 'bg-green-600',
            'target2': 'bg-green-700',
            'invalidation': 'bg-yellow-600',
        };
        // Handle time_stop variants like "time_stop_7d (P&L: -0.5%)"
        let displayReason = reason;
        let colorClass = 'bg-gray-600';
        if (reason.startsWith('time_stop')) {
            displayReason = 'time_stop';
            colorClass = 'bg-orange-600';
        } else {
            colorClass = reasonColors[reason] || 'bg-gray-600';
        }
        return `<span class="${colorClass} px-2 py-0.5 rounded text-xs">${displayReason}</span>`;
    }

    formatUSD(amount) {
        if (amount >= 1000000) {
            return `$${(amount / 1000000).toFixed(2)}M`;
        } else if (amount >= 1000) {
            return `$${(amount / 1000).toFixed(1)}K`;
        }
        return `$${amount.toFixed(2)}`;
    }

    formatKRW(amount) {
        const abs = Math.abs(amount);
        const sign = amount < 0 ? '-' : '';
        if (abs >= 100000000) {
            return `${sign}₩${(abs / 100000000).toFixed(1)}억`;
        } else if (abs >= 10000) {
            return `${sign}₩${(abs / 10000).toFixed(0)}만`;
        }
        return `${sign}₩${Math.round(abs).toLocaleString()}`;
    }

    formatMoney(amount) {
        return this.isKRW() ? this.formatKRW(amount) : this.formatUSD(amount);
    }

    formatPrice(price) {
        if (this.isKRW()) {
            return `₩${Math.round(price).toLocaleString()}`;
        }
        return `$${price.toFixed(2)}`;
    }

    formatQty(qty) {
        if (this.isCrypto()) {
            if (qty >= 100) return qty.toFixed(2);
            if (qty >= 1) return qty.toFixed(4);
            if (qty >= 0.0001) return qty.toFixed(6);
            return qty.toFixed(8);
        }
        return Math.floor(qty).toString();
    }

    // ==================== DCA Methods ====================

    loadDCAForMarket() {
        if (this.isCrypto()) {
            this.loadDCAStatus();
            this.loadDCAFearGreed();
        } else if (this.isKR()) {
            this.loadKRDCAStatus();
        }
    }

    async loadDCAStatus() {
        try {
            const resp = await fetch('/api/dca/status');
            const result = await resp.json();

            const inactive = document.getElementById('dcaInactive');
            const cards = document.querySelectorAll('#panelDca > .grid, #panelDca > .bg-gray-800');

            if (!result.active || !result.data) {
                inactive.classList.remove('hidden');
                cards.forEach(el => { if (el !== inactive) el.classList.add('hidden'); });
                return;
            }

            inactive.classList.add('hidden');
            cards.forEach(el => el.classList.remove('hidden'));

            const d = result.data;

            // F&G display
            const fgValue = document.getElementById('dcaFGValue');
            const fgLabel = document.getElementById('dcaFGLabel');
            const fgFill = document.getElementById('dcaFGFill');
            fgValue.textContent = d.fear_greed || '-';
            fgLabel.textContent = d.fg_label || '-';
            fgFill.style.width = `${d.fear_greed || 50}%`;
            fgFill.className = 'h-2 rounded-full transition-all ' + this.fgColor(d.fear_greed);

            // Restore signal banner
            let banner = document.getElementById('dcaRestoreBanner');
            if (!banner) {
                banner = document.createElement('div');
                banner.id = 'dcaRestoreBanner';
                banner.className = 'hidden mb-3 p-3 rounded-lg border text-sm font-medium';
                const fgCard = fgValue.closest('.grid') || fgValue.closest('.bg-gray-800');
                if (fgCard && fgCard.parentNode) fgCard.parentNode.insertBefore(banner, fgCard.nextSibling);
            }
            if (d.restore_signal) {
                banner.textContent = d.restore_message || 'F&G 회복 — DCA 기본금액 원복 권장';
                banner.className = 'mb-3 p-3 rounded-lg border border-yellow-500 bg-yellow-500/10 text-yellow-300 text-sm font-medium';
                banner.classList.remove('hidden');
            } else if (d.reduced_mode) {
                banner.textContent = `절약 모드: 기본금액 ₩${Math.round(d.base_dca_amount).toLocaleString()} (Extreme Fear 탈출 시 원복 시그널)`;
                banner.className = 'mb-3 p-3 rounded-lg border border-blue-500 bg-blue-500/10 text-blue-300 text-sm font-medium';
                banner.classList.remove('hidden');
            } else {
                banner.classList.add('hidden');
            }

            // Summary cards
            document.getElementById('dcaTotalInvested').textContent = `₩${Math.round(d.total_invested || 0).toLocaleString()}`;
            document.getElementById('dcaCycles').textContent = d.total_dca_cycles || 0;
            document.getElementById('dcaCurrentValue').textContent = `₩${Math.round(d.current_value || 0).toLocaleString()}`;

            const pnl = d.unrealized_pnl || 0;
            const pnlPct = d.unrealized_pct || 0;
            const pnlEl = document.getElementById('dcaPnL');
            pnlEl.textContent = `${pnl >= 0 ? '+' : ''}₩${Math.round(pnl).toLocaleString()} (${pnlPct >= 0 ? '+' : ''}${pnlPct.toFixed(1)}%)`;
            pnlEl.className = `text-sm mt-1 ${pnl >= 0 ? 'text-green-400' : 'text-red-400'}`;

            // Realized PnL (from sells)
            const realPnl = d.realized_pnl || 0;
            const realEl = document.getElementById('dcaRealizedPnL');
            if (realEl) {
                realEl.textContent = `실현: ${realPnl >= 0 ? '+' : ''}₩${Math.round(realPnl).toLocaleString()}`;
                realEl.className = `text-xs mt-0.5 ${realPnl >= 0 ? 'text-green-500' : 'text-red-500'}`;
            }

            // Next DCA
            if (d.next_dca_time) {
                const next = new Date(d.next_dca_time);
                document.getElementById('dcaNextTime').textContent = next.toLocaleString('ko-KR', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
            }
            document.getElementById('dcaMultiplier').textContent = `Multiplier: ${(d.current_multiplier || 1).toFixed(2)}x`;

            // Assets table
            this.renderDCAAssets(d.assets || []);

            // History table
            this.renderDCAHistory(d.history || []);
        } catch (e) {
            console.error('DCA status error:', e);
        }
    }

    fgColor(value) {
        if (value <= 24) return 'bg-red-600';
        if (value <= 44) return 'bg-orange-500';
        if (value <= 55) return 'bg-yellow-500';
        if (value <= 74) return 'bg-green-500';
        return 'bg-green-400';
    }

    renderDCAAssets(assets) {
        const tbody = document.getElementById('dcaAssetsTable');
        if (!assets.length) {
            tbody.innerHTML = '<tr><td colspan="9" class="text-center text-gray-500 py-4">No assets yet</td></tr>';
            return;
        }
        tbody.innerHTML = assets.map(a => {
            const pnlClass = (a.pnl || 0) >= 0 ? 'text-green-400' : 'text-red-400';
            const devClass = Math.abs(a.deviation || 0) > 15 ? 'text-yellow-400 font-bold' : 'text-gray-300';
            const name = (a.symbol || '').replace('KRW-', '');
            return `<tr class="border-b border-gray-700 hover:bg-gray-750">
                <td class="py-2 px-2 font-medium">${name}</td>
                <td class="py-2 px-2 text-right">${(a.target_pct || 0).toFixed(0)}%</td>
                <td class="py-2 px-2 text-right">${(a.current_pct || 0).toFixed(1)}%</td>
                <td class="py-2 px-2 text-right ${devClass}">${(a.deviation || 0) >= 0 ? '+' : ''}${(a.deviation || 0).toFixed(1)}%</td>
                <td class="py-2 px-2 text-right">₩${Math.round(a.total_invested || 0).toLocaleString()}</td>
                <td class="py-2 px-2 text-right">₩${Math.round(a.current_value || 0).toLocaleString()}</td>
                <td class="py-2 px-2 text-right">₩${Math.round(a.avg_cost || 0).toLocaleString()}</td>
                <td class="py-2 px-2 text-right">₩${Math.round(a.current_price || 0).toLocaleString()}</td>
                <td class="py-2 px-2 text-right ${pnlClass}">${(a.pnl || 0) >= 0 ? '+' : ''}₩${Math.round(a.pnl || 0).toLocaleString()} (${(a.pnl_pct || 0) >= 0 ? '+' : ''}${(a.pnl_pct || 0).toFixed(1)}%)</td>
            </tr>`;
        }).join('');
    }

    renderDCAHistory(history) {
        const tbody = document.getElementById('dcaHistoryTable');
        if (!history.length) {
            tbody.innerHTML = '<tr><td colspan="7" class="text-center text-gray-500 py-4">No history yet</td></tr>';
            return;
        }
        // Show newest first
        const sorted = [...history].reverse();
        tbody.innerHTML = sorted.map((h, i) => {
            const date = new Date(h.timestamp);
            const dateStr = date.toLocaleDateString('ko-KR', { month: 'short', day: 'numeric' });
            const fgClass = this.fgColor(h.fear_greed).replace('bg-', 'text-').replace('-600', '-400').replace('-500', '-400');
            const buys = h.buys || [];
            const sells = h.sells || [];
            const rowId = `dca-detail-${i}`;
            let detailRow = '';
            if (buys.length > 0 || sells.length > 0) {
                const items = [
                    ...buys.map(b => `<span class="text-green-400">${b.symbol.replace('KRW-','')} ₩${Math.round(b.amount).toLocaleString()}</span>`),
                    ...sells.map(s => `<span class="text-red-400">${s.symbol.replace('KRW-','')} -₩${Math.round(s.amount).toLocaleString()}</span>`)
                ];
                detailRow = `<tr id="${rowId}" class="hidden border-b border-gray-800">
                    <td colspan="7" class="py-1 px-2 text-xs text-gray-400">${items.join(' | ')}</td>
                </tr>`;
            }
            return `<tr class="border-b border-gray-700 cursor-pointer hover:bg-gray-800" onclick="document.getElementById('${rowId}')?.classList.toggle('hidden')">
                <td class="py-2 px-2">${dateStr}</td>
                <td class="py-2 px-2 text-right ${fgClass}">${h.fear_greed} <span class="text-gray-500 text-xs">${h.fg_label || ''}</span></td>
                <td class="py-2 px-2 text-right">${(h.multiplier || 1).toFixed(2)}x</td>
                <td class="py-2 px-2 text-right">₩${Math.round(h.total_amount || 0).toLocaleString()}</td>
                <td class="py-2 px-2 text-center">${buys.length}</td>
                <td class="py-2 px-2 text-center">${sells.length}</td>
                <td class="py-2 px-2 text-center">${h.rebalanced ? '✓' : '-'}</td>
            </tr>${detailRow}`;
        }).join('');
    }

    async loadDCAFearGreed() {
        try {
            const resp = await fetch('/api/dca/feargreed');
            const data = await resp.json();

            if (data.error) return;

            // Render F&G history bars
            const container = document.getElementById('dcaFGHistory');
            if (!data.history || !data.history.length) {
                container.innerHTML = '<span class="text-gray-500 text-sm">No historical data</span>';
                return;
            }

            const maxH = 108; // px (h-32=128px minus label ~20px)
            const bars = data.history.reverse().map(d => {
                const barH = Math.max(Math.round(d.value / 100 * maxH), 4);
                const color = this.fgColor(d.value);
                const date = new Date(d.timestamp * 1000);
                const label = `${date.getMonth()+1}/${date.getDate()}`;
                return `<div class="flex flex-col items-end justify-end flex-1 min-w-0 h-full" title="${d.classification}: ${d.value}">
                    <div class="w-full ${color} rounded-t" style="height:${barH}px"></div>
                    <div class="text-gray-600 text-xs mt-1 truncate w-full text-center">${label}</div>
                </div>`;
            }).join('');

            container.innerHTML = bars;
        } catch (e) {
            console.error('F&G fetch error:', e);
        }
    }

    // ==================== Scalp Methods ====================

    async loadScalpStatus() {
        try {
            const resp = await fetch('/api/scalp/status');
            const result = await resp.json();

            const inactive = document.getElementById('scalpInactive');
            const panels = document.querySelectorAll('#panelScalp > .grid, #panelScalp > .bg-gray-800:not(#scalpInactive)');

            if (!result.active || !result.data) {
                inactive.classList.remove('hidden');
                panels.forEach(el => el.classList.add('hidden'));
                return;
            }

            inactive.classList.add('hidden');
            panels.forEach(el => el.classList.remove('hidden'));

            const d = result.data;
            const daily = d.daily || {};
            const total = d.total || {};

            // Win Rate card
            const wr = total.win_rate || 0;
            const wrEl = document.getElementById('scalpWinRate');
            wrEl.textContent = `${wr.toFixed(1)}%`;
            wrEl.className = `text-3xl font-bold ${wr >= 55 ? 'text-green-400' : wr >= 45 ? 'text-yellow-400' : 'text-red-400'}`;
            document.getElementById('scalpTotalTrades').textContent = total.trades || 0;

            // Today PnL card
            const dayPnL = daily.net_pnl || 0;
            const dayEl = document.getElementById('scalpDailyPnL');
            dayEl.textContent = `${dayPnL >= 0 ? '+' : ''}₩${Math.round(dayPnL).toLocaleString()}`;
            dayEl.className = `text-2xl font-bold ${dayPnL >= 0 ? 'text-green-400' : 'text-red-400'}`;
            document.getElementById('scalpDailyTrades').textContent = daily.wins || 0;
            document.getElementById('scalpDailyLosses').textContent = daily.losses || 0;

            // Total PnL card
            const totPnL = total.net_pnl || 0;
            const totEl = document.getElementById('scalpTotalPnL');
            totEl.textContent = `${totPnL >= 0 ? '+' : ''}₩${Math.round(totPnL).toLocaleString()}`;
            totEl.className = `text-2xl font-bold ${totPnL >= 0 ? 'text-green-400' : 'text-red-400'}`;
            document.getElementById('scalpStartDate').textContent = total.start_date || '-';

            // Active positions card
            const positions = d.active_positions || {};
            const posCount = Object.keys(positions).length;
            document.getElementById('scalpActiveCount').textContent = posCount;
            document.getElementById('scalpMaxPositions').textContent = d.max_positions || 3;

            // Positions table
            this.renderScalpPositions(positions, d.bar_counter || 0);

            // Today stats detail
            document.getElementById('scalpToday').textContent = daily.date || '-';
            document.getElementById('scalpDayTradeCount').textContent = daily.trades || 0;
            const dayTrades = daily.trades || 0;
            const dayWins = daily.wins || 0;
            document.getElementById('scalpDayWR').textContent = dayTrades > 0 ? `${(dayWins / dayTrades * 100).toFixed(0)}%` : '0%';
            document.getElementById('scalpDayGross').textContent = `₩${Math.round(daily.gross_pnl || 0).toLocaleString()}`;
            document.getElementById('scalpDayComm').textContent = `₩${Math.round(daily.commission || 0).toLocaleString()}`;

            // Lifetime stats
            const bestEl = document.getElementById('scalpBest');
            bestEl.textContent = `₩${Math.round(total.best_trade || 0).toLocaleString()}`;
            bestEl.className = 'text-green-400';
            const worstEl = document.getElementById('scalpWorst');
            worstEl.textContent = `₩${Math.round(total.worst_trade || 0).toLocaleString()}`;
            worstEl.className = 'text-red-400';
            document.getElementById('scalpWinStreak').textContent = total.win_streak_max || 0;
            document.getElementById('scalpLoseStreak').textContent = total.lose_streak_max || 0;
            document.getElementById('scalpTotalGross').textContent = `₩${Math.round(total.gross_pnl || 0).toLocaleString()}`;
            document.getElementById('scalpTotalComm').textContent = `₩${Math.round(total.commission || 0).toLocaleString()}`;

            // Config
            document.getElementById('scalpCandle').textContent = d.candle_min || 15;
            document.getElementById('scalpOrderAmt').textContent = (d.order_amount || 50000).toLocaleString();
            if (d.last_scan) {
                const ls = new Date(d.last_scan);
                document.getElementById('scalpLastScan').textContent = ls.toLocaleTimeString('ko-KR', { hour: '2-digit', minute: '2-digit' });
            }
            if (d.pairs) {
                document.getElementById('scalpPairs').innerHTML = d.pairs.map(p => `<span class="text-blue-400 cursor-pointer hover:underline" onclick="app.openScalpChart('${p}', 'upbit')">${p.replace('KRW-', '')}</span>`).join(', ');
            }

            // Recent trades
            this.renderScalpTrades(d.recent_trades || []);
        } catch (e) {
            console.error('Scalp status error:', e);
        }
    }

    renderScalpTrades(trades) {
        const tbody = document.getElementById('scalpTradesTable');
        if (!trades.length) {
            tbody.innerHTML = '<tr><td colspan="8" class="text-center text-gray-500 py-4">No trades yet</td></tr>';
            return;
        }
        // Show newest first
        const sorted = [...trades].reverse();
        tbody.innerHTML = sorted.map(t => {
            const name = (t.symbol || '').replace('KRW-', '');
            const pnlColor = t.net_pnl >= 0 ? 'text-green-400' : 'text-red-400';
            const pctColor = t.pnl_pct >= 0 ? 'text-green-400' : 'text-red-400';
            const exitTime = t.exit_time ? new Date(t.exit_time).toLocaleString('ko-KR', { month:'numeric', day:'numeric', hour:'2-digit', minute:'2-digit' }) : '-';
            const reason = (t.exit_reason || '').replace('_', ' ');
            const qty = t.quantity ? t.quantity.toFixed(4) : '-';
            return `<tr class="border-b border-gray-700/50 hover:bg-gray-700/30">
                <td class="py-2 px-2 text-gray-300">${exitTime}</td>
                <td class="py-2 px-2 font-medium">${name}</td>
                <td class="py-2 px-2 text-right text-gray-400">${qty}</td>
                <td class="py-2 px-2 text-right text-gray-300">₩${Math.round(t.entry_price).toLocaleString()}</td>
                <td class="py-2 px-2 text-right text-gray-300">₩${Math.round(t.exit_price).toLocaleString()}</td>
                <td class="py-2 px-2 text-right ${pnlColor}">₩${Math.round(t.net_pnl).toLocaleString()}</td>
                <td class="py-2 px-2 text-right ${pctColor}">${t.pnl_pct >= 0 ? '+' : ''}${t.pnl_pct.toFixed(2)}%</td>
                <td class="py-2 px-2 text-gray-400">${reason}</td>
            </tr>`;
        }).join('');
    }

    renderScalpPositions(positions, barCounter) {
        const tbody = document.getElementById('scalpPositionsTable');
        const entries = Object.values(positions);
        if (!entries.length) {
            tbody.innerHTML = '<tr><td colspan="8" class="text-center text-gray-500 py-4">No active positions</td></tr>';
            return;
        }
        tbody.innerHTML = entries.map(p => {
            const name = (p.symbol || '').replace('KRW-', '');
            const barsHeld = barCounter - (p.entry_bar || 0);
            const entryTime = p.entry_time ? new Date(p.entry_time) : null;
            const timeStr = entryTime ? entryTime.toLocaleString('ko-KR', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : '-';
            return `<tr class="border-b border-gray-700 hover:bg-gray-750">
                <td class="py-2 px-2 font-medium text-blue-400 cursor-pointer hover:underline" onclick="app.openScalpChart('${p.symbol}', 'upbit')">${name}</td>
                <td class="py-2 px-2 text-right">₩${Math.round(p.entry_price || 0).toLocaleString()}</td>
                <td class="py-2 px-2 text-right">₩${Math.round(p.amount_krw || 0).toLocaleString()}</td>
                <td class="py-2 px-2 text-right text-red-400">₩${Math.round(p.stop_loss || 0).toLocaleString()}</td>
                <td class="py-2 px-2 text-right text-green-400">₩${Math.round(p.take_profit || 0).toLocaleString()}</td>
                <td class="py-2 px-2 text-right">${(p.rsi_at_entry || 0).toFixed(1)}</td>
                <td class="py-2 px-2 text-right">${barsHeld}</td>
                <td class="py-2 px-2 text-right text-gray-400">${timeStr}</td>
            </tr>`;
        }).join('');
    }

    // ==================== Binance Short Scalp Methods ====================

    async loadBinanceScalpStatus() {
        try {
            const resp = await fetch('/api/binance-scalp/status');
            const result = await resp.json();

            const inactive = document.getElementById('bsInactive');
            const panels = document.querySelectorAll('#panelBinanceScalp > .grid, #panelBinanceScalp > .bg-gray-800:not(#bsInactive)');

            if (!result.active || !result.data) {
                inactive.classList.remove('hidden');
                panels.forEach(el => el.classList.add('hidden'));
                return;
            }

            inactive.classList.add('hidden');
            panels.forEach(el => el.classList.remove('hidden'));

            const d = result.data;
            const daily = d.daily || {};
            const total = d.total || {};
            const fmtUSD = (v) => `$${v.toFixed(2)}`;

            // Win Rate card
            const wr = total.win_rate || 0;
            const wrEl = document.getElementById('bsWinRate');
            wrEl.textContent = `${wr.toFixed(1)}%`;
            wrEl.className = `text-3xl font-bold ${wr >= 55 ? 'text-green-400' : wr >= 45 ? 'text-yellow-400' : 'text-red-400'}`;
            document.getElementById('bsTotalTrades').textContent = total.trades || 0;

            // Today PnL card
            const dayPnL = daily.net_pnl || 0;
            const dayEl = document.getElementById('bsDailyPnL');
            dayEl.textContent = `${dayPnL >= 0 ? '+' : ''}${fmtUSD(dayPnL)}`;
            dayEl.className = `text-2xl font-bold ${dayPnL >= 0 ? 'text-green-400' : 'text-red-400'}`;
            document.getElementById('bsDailyWins').textContent = daily.wins || 0;
            document.getElementById('bsDailyLosses').textContent = daily.losses || 0;

            // Total PnL card
            const totPnL = total.net_pnl || 0;
            const totEl = document.getElementById('bsTotalPnL');
            totEl.textContent = `${totPnL >= 0 ? '+' : ''}${fmtUSD(totPnL)}`;
            totEl.className = `text-2xl font-bold ${totPnL >= 0 ? 'text-green-400' : 'text-red-400'}`;
            document.getElementById('bsStartDate').textContent = total.start_date || '-';

            // Active positions card
            const positions = d.active_positions || {};
            const posCount = Object.keys(positions).length;
            document.getElementById('bsActiveCount').textContent = posCount;
            document.getElementById('bsMaxPositions').textContent = d.max_positions || 3;

            // Positions table
            this.renderBinanceScalpPositions(positions, d.bar_counter || 0);

            // Today stats detail
            document.getElementById('bsToday').textContent = daily.date || '-';
            document.getElementById('bsDayTradeCount').textContent = daily.trades || 0;
            const dayTrades = daily.trades || 0;
            const dayWins = daily.wins || 0;
            document.getElementById('bsDayWR').textContent = dayTrades > 0 ? `${(dayWins / dayTrades * 100).toFixed(0)}%` : '0%';
            document.getElementById('bsDayGross').textContent = fmtUSD(daily.gross_pnl || 0);
            document.getElementById('bsDayComm').textContent = fmtUSD(daily.commission || 0);

            // Lifetime stats
            document.getElementById('bsBest').textContent = fmtUSD(total.best_trade || 0);
            document.getElementById('bsWorst').textContent = fmtUSD(total.worst_trade || 0);
            document.getElementById('bsWinStreak').textContent = total.win_streak_max || 0;
            document.getElementById('bsLoseStreak').textContent = total.lose_streak_max || 0;
            document.getElementById('bsTotalGross').textContent = fmtUSD(total.gross_pnl || 0);
            document.getElementById('bsTotalComm').textContent = fmtUSD(total.commission || 0);
            document.getElementById('bsFundingEarned').textContent = fmtUSD(d.funding_earned || 0);

            // Config
            document.getElementById('bsCandle').textContent = d.candle_min || 5;
            document.getElementById('bsOrderAmt').textContent = (d.order_amount || 80).toFixed(0);
            document.getElementById('bsLeverage').textContent = d.leverage || 2;
            if (d.last_scan) {
                const ls = new Date(d.last_scan);
                document.getElementById('bsLastScan').textContent = ls.toLocaleTimeString('ko-KR', { hour: '2-digit', minute: '2-digit' });
            }
            if (d.pairs) {
                document.getElementById('bsPairs').innerHTML = d.pairs.map(p => `<span class="text-red-400 cursor-pointer hover:underline" onclick="app.openScalpChart('${p}', 'binance')">${p}</span>`).join(', ');
            }

            // Recent trades
            this.renderBinanceScalpTrades(d.recent_trades || []);
        } catch (e) {
            console.error('Binance scalp status error:', e);
        }
    }

    renderBinanceScalpPositions(positions, barCounter) {
        const tbody = document.getElementById('bsPositionsTable');
        const entries = Object.values(positions);
        if (!entries.length) {
            tbody.innerHTML = '<tr><td colspan="8" class="text-center text-gray-500 py-4">No active positions</td></tr>';
            return;
        }
        tbody.innerHTML = entries.map(p => {
            const entryTime = p.entry_time ? new Date(p.entry_time) : null;
            const timeStr = entryTime ? entryTime.toLocaleString('ko-KR', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : '-';
            return `<tr class="border-b border-gray-700 hover:bg-gray-750">
                <td class="py-2 px-2 font-medium text-red-400 cursor-pointer hover:underline" onclick="app.openScalpChart('${p.symbol}', 'binance')">${p.symbol || ''}</td>
                <td class="py-2 px-2 text-right">$${(p.entry_price || 0).toFixed(4)}</td>
                <td class="py-2 px-2 text-right">$${(p.amount_usdt || 0).toFixed(2)}</td>
                <td class="py-2 px-2 text-right">${p.leverage || 2}x</td>
                <td class="py-2 px-2 text-right text-red-400">$${(p.stop_loss || 0).toFixed(4)}</td>
                <td class="py-2 px-2 text-right text-green-400">$${(p.take_profit || 0).toFixed(4)}</td>
                <td class="py-2 px-2 text-right">${(p.rsi_at_entry || 0).toFixed(1)}</td>
                <td class="py-2 px-2 text-right text-gray-400">${timeStr}</td>
            </tr>`;
        }).join('');
    }

    renderBinanceScalpTrades(trades) {
        const tbody = document.getElementById('bsTradesTable');
        if (!trades.length) {
            tbody.innerHTML = '<tr><td colspan="7" class="text-center text-gray-500 py-4">No trades yet</td></tr>';
            return;
        }
        const sorted = [...trades].reverse();
        tbody.innerHTML = sorted.map(t => {
            const pnlColor = t.net_pnl >= 0 ? 'text-green-400' : 'text-red-400';
            const pctColor = t.pnl_pct >= 0 ? 'text-green-400' : 'text-red-400';
            const exitTime = t.exit_time ? new Date(t.exit_time).toLocaleString('ko-KR', { month:'numeric', day:'numeric', hour:'2-digit', minute:'2-digit' }) : '-';
            const reason = (t.exit_reason || '').replace(/_/g, ' ');
            return `<tr class="border-b border-gray-700/50 hover:bg-gray-700/30">
                <td class="py-2 px-2 text-gray-300">${exitTime}</td>
                <td class="py-2 px-2 font-medium">${t.symbol || ''}</td>
                <td class="py-2 px-2 text-right text-gray-300">$${(t.entry_price || 0).toFixed(4)}</td>
                <td class="py-2 px-2 text-right text-gray-300">$${(t.exit_price || 0).toFixed(4)}</td>
                <td class="py-2 px-2 text-right ${pnlColor}">$${(t.net_pnl || 0).toFixed(2)}</td>
                <td class="py-2 px-2 text-right ${pctColor}">${t.pnl_pct >= 0 ? '+' : ''}${(t.pnl_pct || 0).toFixed(2)}%</td>
                <td class="py-2 px-2 text-gray-400">${reason}</td>
            </tr>`;
        }).join('');
    }

    // ==================== BTC Futures Methods ====================

    async loadBTCFuturesStatus() {
        try {
            const resp = await fetch('/api/btc-futures/status');
            const result = await resp.json();

            const inactive = document.getElementById('bfInactive');
            const panels = document.querySelectorAll('#panelBtcFutures > .grid, #panelBtcFutures > .bg-gray-800:not(#bfInactive)');

            if (!result.active || !result.data) {
                inactive.classList.remove('hidden');
                panels.forEach(el => el.classList.add('hidden'));
                return;
            }

            inactive.classList.add('hidden');
            panels.forEach(el => el.classList.remove('hidden'));

            const d = result.data;
            const daily = d.daily || {};
            const total = d.total || {};
            const fmtUSD = (v) => `$${v.toFixed(2)}`;

            // Win Rate card
            const wr = total.win_rate || 0;
            const wrEl = document.getElementById('bfWinRate');
            wrEl.textContent = `${wr.toFixed(1)}%`;
            wrEl.className = `text-3xl font-bold ${wr >= 55 ? 'text-green-400' : wr >= 45 ? 'text-yellow-400' : 'text-red-400'}`;
            document.getElementById('bfTotalTrades').textContent = total.trades || 0;

            // Today PnL card
            const dayPnL = daily.net_pnl || 0;
            const dayEl = document.getElementById('bfDailyPnL');
            dayEl.textContent = `${dayPnL >= 0 ? '+' : ''}${fmtUSD(dayPnL)}`;
            dayEl.className = `text-2xl font-bold ${dayPnL >= 0 ? 'text-green-400' : 'text-red-400'}`;
            document.getElementById('bfDailyWins').textContent = daily.wins || 0;
            document.getElementById('bfDailyLosses').textContent = daily.losses || 0;

            // Total PnL card
            const totPnL = total.net_pnl || 0;
            const totEl = document.getElementById('bfTotalPnL');
            totEl.textContent = `${totPnL >= 0 ? '+' : ''}${fmtUSD(totPnL)}`;
            totEl.className = `text-2xl font-bold ${totPnL >= 0 ? 'text-green-400' : 'text-red-400'}`;
            document.getElementById('bfStartDate').textContent = total.start_date || '-';

            // Position status card
            const pos = d.active_position;
            const posEl = document.getElementById('bfPositionStatus');
            if (pos) {
                posEl.textContent = 'LONG';
                posEl.className = 'text-2xl font-bold text-green-400';
            } else {
                posEl.textContent = 'None';
                posEl.className = 'text-2xl font-bold text-gray-500';
            }
            if (d.last_scan) {
                const ls = new Date(d.last_scan);
                document.getElementById('bfLastScan').textContent = ls.toLocaleTimeString('ko-KR', { hour: '2-digit', minute: '2-digit' });
            }

            // Position table
            const posTbody = document.getElementById('bfPositionTable');
            const posInfo = document.getElementById('bfPositionInfo');
            if (pos) {
                posInfo.classList.add('hidden');
                const entryTime = pos.entry_time ? new Date(pos.entry_time) : null;
                const timeStr = entryTime ? entryTime.toLocaleString('ko-KR', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : '-';
                posTbody.innerHTML = `<tr class="border-b border-gray-700 hover:bg-gray-750">
                    <td class="py-2 px-2 font-medium text-green-400">${pos.symbol || 'BTCUSDT'}</td>
                    <td class="py-2 px-2 text-right">$${(pos.entry_price || 0).toFixed(1)}</td>
                    <td class="py-2 px-2 text-right">$${(pos.amount_usdt || 0).toFixed(2)}</td>
                    <td class="py-2 px-2 text-right">${pos.leverage || 2}x</td>
                    <td class="py-2 px-2 text-right text-red-400">$${(pos.stop_loss || 0).toFixed(1)}</td>
                    <td class="py-2 px-2 text-right text-green-400">$${(pos.take_profit || 0).toFixed(1)}</td>
                    <td class="py-2 px-2 text-right text-yellow-400">${((pos.entry_funding || 0) * 100).toFixed(4)}%</td>
                    <td class="py-2 px-2 text-right">${(pos.entry_rsi || 0).toFixed(1)}</td>
                    <td class="py-2 px-2 text-right text-gray-400">${timeStr}</td>
                </tr>`;
            } else {
                posInfo.classList.remove('hidden');
                posTbody.innerHTML = '';
            }

            // Today stats detail
            document.getElementById('bfToday').textContent = daily.date || '-';
            document.getElementById('bfDayTradeCount').textContent = daily.trades || 0;
            document.getElementById('bfDayNet').textContent = fmtUSD(daily.net_pnl || 0);
            document.getElementById('bfDayComm').textContent = fmtUSD(daily.commission || 0);

            // Lifetime stats
            document.getElementById('bfBest').textContent = fmtUSD(total.best_trade || 0);
            document.getElementById('bfWorst').textContent = fmtUSD(total.worst_trade || 0);
            document.getElementById('bfTotalComm').textContent = fmtUSD(total.commission || 0);

            // Config
            document.getElementById('bfSymbol').textContent = d.symbol || 'BTCUSDT';
            document.getElementById('bfCandle').textContent = d.candle_min || 15;
            document.getElementById('bfOrderAmt').textContent = (d.order_amount || 80).toFixed(0);
            document.getElementById('bfLeverage').textContent = d.leverage || 2;
            document.getElementById('bfFundingThresh').textContent = `${(d.funding_thresh || -0.01).toFixed(2)}%`;
            document.getElementById('bfRSIMin').textContent = d.rsi_min || 40;
            document.getElementById('bfTPAtr').textContent = d.tp_atr_mult || 2.0;
            document.getElementById('bfSLAtr').textContent = d.sl_atr_mult || 1.5;

            // Recent trades
            this.renderBTCFuturesTrades(d.recent_trades || []);
        } catch (e) {
            console.error('BTC Futures status error:', e);
        }
    }

    // BTC Futures chart instances (for cleanup)
    _bfCharts = {};

    initBTCFuturesChartEvents() {
        const select = document.getElementById('bfChartDays');
        if (select && !select._bound) {
            select.addEventListener('change', () => this.loadBTCFuturesChartData());
            select._bound = true;
        }
    }

    async loadBTCFuturesChartData() {
        const days = document.getElementById('bfChartDays')?.value || '1';
        try {
            const resp = await fetch(`/api/btc-futures/chart-data?days=${days}`);
            const data = await resp.json();
            const hasData = (data.scans?.length > 0) || (data.signals?.length > 0);
            document.getElementById('bfNoChartData').classList.toggle('hidden', hasData);
            if (hasData) {
                this.renderBTCFuturesCharts(data);
            }
        } catch (e) {
            console.error('BTC Futures chart data error:', e);
        }
    }

    destroyBFChart(id) {
        if (this._bfCharts[id]) {
            this._bfCharts[id].remove();
            delete this._bfCharts[id];
        }
    }

    createBFChart(containerId, height) {
        this.destroyBFChart(containerId);
        const el = document.getElementById(containerId);
        if (!el) return null;
        el.innerHTML = '';
        const chart = LightweightCharts.createChart(el, {
            width: el.clientWidth,
            height: height || 300,
            layout: { background: { color: '#1f2937' }, textColor: '#9ca3af' },
            grid: { vertLines: { color: '#374151' }, horzLines: { color: '#374151' } },
            crosshair: { mode: LightweightCharts.CrosshairMode.Normal },
            timeScale: { timeVisible: true, secondsVisible: false },
            rightPriceScale: { borderColor: '#374151' },
        });
        this._bfCharts[containerId] = chart;
        const ro = new ResizeObserver(() => chart.applyOptions({ width: el.clientWidth }));
        ro.observe(el);
        return chart;
    }

    renderBTCFuturesCharts(data) {
        // Deduplicate by time (lightweight-charts requires unique ascending times)
        const dedup = (arr) => {
            const seen = new Set();
            return arr.filter(s => {
                if (seen.has(s.time)) return false;
                seen.add(s.time);
                return true;
            }).sort((a, b) => a.time - b.time);
        };
        const scans = dedup(data.scans || []);
        const signals = dedup(data.signals || []);
        const trades = data.trades || [];

        // 1. Price chart + EMA50 + trade markers
        if (scans.length > 0) {
            const chart = this.createBFChart('bfChartPrice', 300);
            if (chart) {
                const priceSeries = chart.addLineSeries({ color: '#3b82f6', lineWidth: 2, title: 'BTC' });
                priceSeries.setData(scans.map(s => ({ time: s.time, value: s.price })));

                const emaSeries = chart.addLineSeries({ color: '#f59e0b', lineWidth: 1, lineStyle: 2, title: 'EMA50' });
                emaSeries.setData(scans.filter(s => s.ema50 > 0).map(s => ({ time: s.time, value: s.ema50 })));

                // Trade entry markers (green up) and exit markers (red down)
                const markers = [];
                for (const t of trades) {
                    markers.push({
                        time: t.entry_time, position: 'belowBar', color: '#22c55e',
                        shape: 'arrowUp', text: `Buy $${t.entry_price.toFixed(0)}`
                    });
                    markers.push({
                        time: t.exit_time, position: 'aboveBar',
                        color: t.net_pnl >= 0 ? '#22c55e' : '#ef4444',
                        shape: 'arrowDown', text: `${t.net_pnl >= 0 ? '+' : ''}$${t.net_pnl.toFixed(2)}`
                    });
                }
                // Signal entry points (no trade executed)
                for (const s of scans) {
                    if (s.signal === 'long') {
                        markers.push({
                            time: s.time, position: 'belowBar', color: '#a78bfa',
                            shape: 'circle', text: 'Signal'
                        });
                    } else if (s.signal === 'filtered_oi') {
                        markers.push({
                            time: s.time, position: 'aboveBar', color: '#f97316',
                            shape: 'square', text: 'OI Block'
                        });
                    }
                }
                if (markers.length > 0) {
                    markers.sort((a, b) => a.time - b.time);
                    priceSeries.setMarkers(markers);
                }
                chart.timeScale().fitContent();
            }
        }

        // 2. RSI chart
        if (scans.length > 0) {
            const chart = this.createBFChart('bfChartRSI', 200);
            if (chart) {
                const rsiSeries = chart.addLineSeries({ color: '#a78bfa', lineWidth: 2, title: 'RSI(7)' });
                rsiSeries.setData(scans.filter(s => s.rsi > 0).map(s => ({ time: s.time, value: s.rsi })));

                // Threshold lines
                const rsi40 = chart.addLineSeries({ color: '#ef4444', lineWidth: 1, lineStyle: 2, title: 'Min(40)' });
                const rsi70 = chart.addLineSeries({ color: '#22c55e', lineWidth: 1, lineStyle: 2 });
                const rsi30 = chart.addLineSeries({ color: '#f59e0b', lineWidth: 1, lineStyle: 2 });
                const times = scans.filter(s => s.rsi > 0).map(s => s.time);
                if (times.length > 0) {
                    rsi40.setData(times.map(t => ({ time: t, value: 40 })));
                    rsi70.setData(times.map(t => ({ time: t, value: 70 })));
                    rsi30.setData(times.map(t => ({ time: t, value: 30 })));
                }
                chart.timeScale().fitContent();
            }
        }

        // 3. Funding Rate chart
        if (scans.length > 0) {
            const chart = this.createBFChart('bfChartFunding', 200);
            if (chart) {
                const fundingSeries = chart.addLineSeries({ color: '#f59e0b', lineWidth: 2, title: 'Funding %' });
                fundingSeries.setData(scans.map(s => ({ time: s.time, value: s.funding * 100 })));

                // Threshold line at -0.01%
                const threshSeries = chart.addLineSeries({ color: '#ef4444', lineWidth: 1, lineStyle: 2, title: '-0.01%' });
                const times = scans.map(s => s.time);
                if (times.length > 0) {
                    threshSeries.setData(times.map(t => ({ time: t, value: -0.01 })));
                }

                // Zero line
                const zeroSeries = chart.addLineSeries({ color: '#6b7280', lineWidth: 1, lineStyle: 2 });
                if (times.length > 0) {
                    zeroSeries.setData(times.map(t => ({ time: t, value: 0 })));
                }
                chart.timeScale().fitContent();
            }
        }

        // 4. Volume chart (filter hold rows with volume=0)
        const volScans = scans.filter(s => s.volume > 0);
        if (volScans.length > 0) {
            const chart = this.createBFChart('bfChartVolume', 180);
            if (chart) {
                const volSeries = chart.addHistogramSeries({
                    color: '#3b82f6', priceFormat: { type: 'volume' }, title: 'Volume'
                });
                volSeries.setData(volScans.map(s => ({
                    time: s.time, value: s.volume,
                    color: s.volume > s.avg_volume * 1.5 ? '#22c55e' : '#3b82f680'
                })));

                // Average volume line
                const avgSeries = chart.addLineSeries({ color: '#f59e0b', lineWidth: 1, lineStyle: 2, title: 'Avg' });
                avgSeries.setData(volScans.filter(s => s.avg_volume > 0).map(s => ({ time: s.time, value: s.avg_volume })));
                chart.timeScale().fitContent();
            }
        }

        // 5. OBI + Taker Buy Ratio chart
        if (signals.length > 0) {
            const chart = this.createBFChart('bfChartOBI', 200);
            if (chart) {
                const obi5Series = chart.addLineSeries({ color: '#3b82f6', lineWidth: 2, title: 'OBI-5' });
                obi5Series.setData(signals.map(s => ({ time: s.time, value: s.obi5 })));

                const obi20Series = chart.addLineSeries({ color: '#8b5cf6', lineWidth: 1, title: 'OBI-20' });
                obi20Series.setData(signals.map(s => ({ time: s.time, value: s.obi20 })));

                const takerSeries = chart.addLineSeries({ color: '#f59e0b', lineWidth: 1, lineStyle: 2, title: 'Taker Buy' });
                takerSeries.setData(signals.map(s => ({ time: s.time, value: s.taker_buy })));

                // 0.5 baseline
                const baseSeries = chart.addLineSeries({ color: '#6b7280', lineWidth: 1, lineStyle: 2 });
                const times = signals.map(s => s.time);
                if (times.length > 0) {
                    baseSeries.setData(times.map(t => ({ time: t, value: 0.5 })));
                }
                chart.timeScale().fitContent();
            }
        }

        // 6. Cumulative PnL chart
        if (trades.length > 0) {
            const chart = this.createBFChart('bfChartPnL', 200);
            if (chart) {
                const pnlSeries = chart.addAreaSeries({
                    topColor: 'rgba(34,197,94,0.3)', bottomColor: 'rgba(34,197,94,0.0)',
                    lineColor: '#22c55e', lineWidth: 2, title: 'Cumulative P&L'
                });
                pnlSeries.setData(trades.map(t => ({ time: t.exit_time, value: t.cum_pnl })));

                // Zero line
                const zeroSeries = chart.addLineSeries({ color: '#6b7280', lineWidth: 1, lineStyle: 2 });
                const times = trades.map(t => t.exit_time);
                if (times.length > 0) {
                    zeroSeries.setData(times.map(t => ({ time: t, value: 0 })));
                }
                chart.timeScale().fitContent();
            }
        }
    }

    renderBTCFuturesTrades(trades) {
        const tbody = document.getElementById('bfTradesTable');
        if (!trades.length) {
            tbody.innerHTML = '<tr><td colspan="8" class="text-center text-gray-500 py-4">No trades yet</td></tr>';
            return;
        }
        const sorted = [...trades].reverse();
        tbody.innerHTML = sorted.map(t => {
            const pnlColor = t.net_pnl >= 0 ? 'text-green-400' : 'text-red-400';
            const pctColor = t.pnl_pct >= 0 ? 'text-green-400' : 'text-red-400';
            const exitTime = t.exit_time ? new Date(t.exit_time).toLocaleString('ko-KR', { month:'numeric', day:'numeric', hour:'2-digit', minute:'2-digit' }) : '-';
            const reason = (t.exit_reason || '').replace(/_/g, ' ');
            return `<tr class="border-b border-gray-700/50 hover:bg-gray-700/30">
                <td class="py-2 px-2 text-gray-300">${exitTime}</td>
                <td class="py-2 px-2 text-right text-gray-300">$${(t.entry_price || 0).toFixed(1)}</td>
                <td class="py-2 px-2 text-right text-gray-300">$${(t.exit_price || 0).toFixed(1)}</td>
                <td class="py-2 px-2 text-right ${pnlColor}">$${(t.net_pnl || 0).toFixed(2)}</td>
                <td class="py-2 px-2 text-right ${pctColor}">${t.pnl_pct >= 0 ? '+' : ''}${(t.pnl_pct || 0).toFixed(2)}%</td>
                <td class="py-2 px-2 text-right text-yellow-400">${((t.entry_funding || 0) * 100).toFixed(4)}%</td>
                <td class="py-2 px-2 text-right">${(t.entry_rsi || 0).toFixed(1)}</td>
                <td class="py-2 px-2 text-gray-400">${reason}</td>
            </tr>`;
        }).join('');
    }

    // ==================== Binance Funding Arb Methods ====================

    async loadBinanceArbStatus() {
        try {
            const resp = await fetch('/api/binance-arb/status');
            const result = await resp.json();

            const inactive = document.getElementById('baInactive');
            const panels = document.querySelectorAll('#panelBinanceArb > .grid, #panelBinanceArb > .bg-gray-800:not(#baInactive)');

            if (!result.active || !result.data) {
                inactive.classList.remove('hidden');
                panels.forEach(el => el.classList.add('hidden'));
                return;
            }

            inactive.classList.add('hidden');
            panels.forEach(el => el.classList.remove('hidden'));

            const d = result.data;
            const total = d.total || {};
            const fmtUSD = (v) => `$${v.toFixed(2)}`;

            // Funding Earned card
            const funding = total.total_funding || 0;
            document.getElementById('baFundingEarned').textContent = fmtUSD(funding);
            document.getElementById('baTrades').textContent = total.trades || 0;

            // Net PnL card
            const netPnL = total.net_pnl || 0;
            const netEl = document.getElementById('baNetPnL');
            netEl.textContent = `${netPnL >= 0 ? '+' : ''}${fmtUSD(netPnL)}`;
            netEl.className = `text-2xl font-bold ${netPnL >= 0 ? 'text-green-400' : 'text-red-400'}`;

            // Commission card
            document.getElementById('baCommission').textContent = fmtUSD(total.commission || 0);
            document.getElementById('baStartDate').textContent = total.start_date || '-';

            // Active positions card
            const positions = d.active_positions || {};
            const posCount = Object.keys(positions).length;
            document.getElementById('baActiveCount').textContent = posCount;
            document.getElementById('baMaxCap').textContent = (d.max_capital || 150).toFixed(0);

            // Funding rates
            this.renderFundingRates(d.last_funding_rates || {}, d.min_funding_rate || 0.0001);

            // Active positions table
            this.renderArbPositions(positions);

            // Strategy params
            document.getElementById('baMinRate').textContent = `${((d.min_funding_rate || 0.0001) * 100).toFixed(2)}%`;
            document.getElementById('baMaxCapital').textContent = (d.max_capital || 150).toFixed(0);
            if (d.last_check) {
                const lc = new Date(d.last_check);
                document.getElementById('baLastCheck').textContent = lc.toLocaleTimeString('ko-KR', { hour: '2-digit', minute: '2-digit' });
            }
            if (d.pairs) {
                document.getElementById('baPairs').textContent = d.pairs.join(', ');
            }

            // Recent trades
            this.renderArbTrades(d.recent_trades || []);
        } catch (e) {
            console.error('Binance arb status error:', e);
        }
    }

    renderFundingRates(rates, minRate) {
        const container = document.getElementById('baFundingRates');
        const entries = Object.entries(rates);
        if (!entries.length) {
            container.innerHTML = '<div class="text-gray-500 text-center py-4 col-span-4">No funding rate data</div>';
            return;
        }
        container.innerHTML = entries.map(([symbol, rate]) => {
            const pct = (rate * 100).toFixed(4);
            const isAboveMin = rate >= minRate;
            const color = rate > 0 ? (isAboveMin ? 'text-green-400' : 'text-yellow-400') : 'text-red-400';
            const bgColor = rate > 0 ? (isAboveMin ? 'bg-green-900/20 border-green-700/30' : 'bg-yellow-900/20 border-yellow-700/30') : 'bg-red-900/20 border-red-700/30';
            const annualized = (rate * 3 * 365 * 100).toFixed(1);
            return `<div class="rounded p-3 border ${bgColor}">
                <div class="text-gray-300 font-medium">${symbol}</div>
                <div class="${color} text-xl font-bold">${pct}%</div>
                <div class="text-gray-500 text-xs">~${annualized}% APR</div>
                ${isAboveMin ? '<div class="text-green-500 text-xs mt-1">Above threshold</div>' : '<div class="text-gray-500 text-xs mt-1">Below min ${(minRate * 100).toFixed(2)}%</div>'}
            </div>`;
        }).join('');
    }

    renderArbPositions(positions) {
        const tbody = document.getElementById('baPositionsTable');
        const entries = Object.values(positions);
        if (!entries.length) {
            tbody.innerHTML = '<tr><td colspan="8" class="text-center text-gray-500 py-4">No active arb positions</td></tr>';
            return;
        }
        tbody.innerHTML = entries.map(p => {
            const openedAt = p.opened_at ? new Date(p.opened_at) : null;
            const duration = openedAt ? this.formatDuration(Date.now() - openedAt.getTime()) : '-';
            return `<tr class="border-b border-gray-700 hover:bg-gray-750">
                <td class="py-2 px-2 font-medium text-blue-400">${p.symbol || ''}</td>
                <td class="py-2 px-2 text-right">$${(p.capital_used || 0).toFixed(2)}</td>
                <td class="py-2 px-2 text-right">$${(p.spot_entry_price || 0).toFixed(2)}</td>
                <td class="py-2 px-2 text-right">$${(p.futures_entry || 0).toFixed(2)}</td>
                <td class="py-2 px-2 text-right text-gray-300">$${(p.basis || 0).toFixed(4)}</td>
                <td class="py-2 px-2 text-right text-yellow-400">$${(p.funding_collected || 0).toFixed(4)}</td>
                <td class="py-2 px-2 text-right">${p.funding_payments || 0}</td>
                <td class="py-2 px-2 text-right text-gray-400">${duration}</td>
            </tr>`;
        }).join('');
    }

    renderArbTrades(trades) {
        const tbody = document.getElementById('baTradesTable');
        if (!trades.length) {
            tbody.innerHTML = '<tr><td colspan="8" class="text-center text-gray-500 py-4">No trades yet</td></tr>';
            return;
        }
        const sorted = [...trades].reverse();
        tbody.innerHTML = sorted.map(t => {
            const pnlColor = t.net_pnl >= 0 ? 'text-green-400' : 'text-red-400';
            const closedAt = t.closed_at ? new Date(t.closed_at).toLocaleString('ko-KR', { month:'numeric', day:'numeric', hour:'2-digit', minute:'2-digit' }) : '-';
            const basisPnL = (t.net_pnl || 0) - (t.funding_collected || 0) + (t.total_commission || 0);
            const reason = (t.exit_reason || '').replace(/_/g, ' ');
            return `<tr class="border-b border-gray-700/50 hover:bg-gray-700/30">
                <td class="py-2 px-2 text-gray-300">${closedAt}</td>
                <td class="py-2 px-2 font-medium">${t.symbol || ''}</td>
                <td class="py-2 px-2 text-right text-gray-300">$${(t.capital_used || 0).toFixed(2)}</td>
                <td class="py-2 px-2 text-right text-yellow-400">$${(t.funding_collected || 0).toFixed(4)}</td>
                <td class="py-2 px-2 text-right text-gray-300">$${basisPnL.toFixed(4)}</td>
                <td class="py-2 px-2 text-right ${pnlColor}">$${(t.net_pnl || 0).toFixed(4)}</td>
                <td class="py-2 px-2 text-right text-gray-400">${t.hold_duration || '-'}</td>
                <td class="py-2 px-2 text-gray-400">${reason}</td>
            </tr>`;
        }).join('');
    }

    formatDuration(ms) {
        const hours = Math.floor(ms / 3600000);
        if (hours < 24) return `${hours}h`;
        const days = Math.floor(hours / 24);
        const remainHours = hours % 24;
        return `${days}d ${remainHours}h`;
    }

    // ==================== KR DCA Methods ====================

    async loadKRDCAStatus() {
        try {
            const resp = await fetch('/api/kr-dca/status');
            const result = await resp.json();

            const inactive = document.getElementById('krDcaInactive');
            const panels = document.querySelectorAll('#panelKrDca > .grid, #panelKrDca > .bg-gray-800:not(#krDcaInactive)');

            if (!result.active || !result.data) {
                inactive.classList.remove('hidden');
                panels.forEach(el => el.classList.add('hidden'));
                return;
            }

            inactive.classList.add('hidden');
            panels.forEach(el => el.classList.remove('hidden'));

            const d = result.data;

            // RSI card
            const rsi = d.rsi || 0;
            const rsiEl = document.getElementById('krDcaRSI');
            rsiEl.textContent = rsi.toFixed(1);
            rsiEl.className = `text-3xl font-bold ${rsi < 30 ? 'text-green-400' : rsi < 50 ? 'text-yellow-400' : rsi < 65 ? 'text-white' : 'text-red-400'}`;
            document.getElementById('krDcaRSILabel').textContent = d.rsi_label || '-';

            // RSI fill bar
            const rsiFill = document.getElementById('krDcaRSIFill');
            rsiFill.style.width = `${Math.min(100, rsi)}%`;
            rsiFill.className = `h-2 rounded-full transition-all ${rsi < 30 ? 'bg-green-500' : rsi < 50 ? 'bg-yellow-500' : rsi < 65 ? 'bg-gray-400' : 'bg-red-500'}`;

            // Total Invested
            document.getElementById('krDcaTotalInvested').textContent = `₩${Math.round(d.total_invested || 0).toLocaleString()}`;
            document.getElementById('krDcaCycles').textContent = d.total_dca_cycles || 0;
            document.getElementById('krDcaShares').textContent = Math.round(d.total_shares || 0);

            // Current Value + PnL
            const cv = d.current_value || 0;
            document.getElementById('krDcaCurrentValue').textContent = cv > 0 ? `₩${Math.round(cv).toLocaleString()}` : '-';
            const pnl = d.unrealized_pnl || 0;
            const pnlPct = d.unrealized_pct || 0;
            const pnlEl = document.getElementById('krDcaPnL');
            pnlEl.textContent = cv > 0 ? `${pnl >= 0 ? '+' : ''}₩${Math.round(pnl).toLocaleString()} (${pnlPct >= 0 ? '+' : ''}${pnlPct.toFixed(1)}%)` : '-';
            pnlEl.className = `text-sm mt-1 ${pnl >= 0 ? 'text-green-400' : 'text-red-400'}`;

            // Next DCA
            if (d.next_dca_time) {
                const next = new Date(d.next_dca_time);
                document.getElementById('krDcaNextTime').textContent = next.toLocaleString('ko-KR', { month: 'short', day: 'numeric', weekday: 'short', hour: '2-digit', minute: '2-digit' });
            }
            const actionLabel = d.current_action === 'buy' ? `Buy ${d.current_shares || 0} shares` : d.current_action === 'sell' ? 'Sell signal' : 'Skip';
            document.getElementById('krDcaAction').textContent = `Action: ${actionLabel}`;

            // Price / Avg Cost / EMA50
            const price = d.current_price || 0;
            document.getElementById('krDcaPrice').textContent = price > 0 ? `₩${Math.round(price).toLocaleString()}` : '-';
            document.getElementById('krDcaAvgCost').textContent = d.avg_cost > 0 ? `₩${Math.round(d.avg_cost).toLocaleString()}` : '-';
            document.getElementById('krDcaEMA50').textContent = d.ema50 > 0 ? `₩${Math.round(d.ema50).toLocaleString()}` : '-';
            const ema50Status = d.price_vs_ema50 === 'below' ? 'Price < EMA50 (+1 bonus share)' : d.price_vs_ema50 === 'above' ? 'Price > EMA50' : '-';
            const ema50El = document.getElementById('krDcaEMA50Status');
            ema50El.textContent = ema50Status;
            ema50El.className = `text-xs mt-1 ${d.price_vs_ema50 === 'below' ? 'text-green-400' : 'text-gray-500'}`;

            // History table
            this.renderKRDCAHistory(d.history || []);
        } catch (e) {
            console.error('KR DCA status error:', e);
        }
    }

    renderKRDCAHistory(history) {
        const tbody = document.getElementById('krDcaHistoryTable');
        if (!history || !history.length) {
            tbody.innerHTML = '<tr><td colspan="7" class="text-center text-gray-500 py-4">No history yet</td></tr>';
            return;
        }
        const sorted = [...history].reverse();
        tbody.innerHTML = sorted.map(h => {
            const date = new Date(h.timestamp).toLocaleString('ko-KR', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
            const rsiColor = h.rsi < 30 ? 'text-green-400' : h.rsi < 50 ? 'text-yellow-400' : h.rsi < 65 ? 'text-white' : 'text-red-400';
            const actionColor = h.action === 'buy' ? 'text-blue-400' : h.action === 'sell' ? 'text-red-400' : 'text-gray-400';
            return `<tr class="border-b border-gray-700/50 hover:bg-gray-700/30">
                <td class="py-2 px-2 text-gray-300">${date}</td>
                <td class="py-2 px-2 text-right ${rsiColor}">${h.rsi.toFixed(1)}</td>
                <td class="py-2 px-2 text-center ${actionColor}">${h.action}</td>
                <td class="py-2 px-2 text-right">${h.shares || 0}</td>
                <td class="py-2 px-2 text-right">₩${Math.round(h.price || 0).toLocaleString()}</td>
                <td class="py-2 px-2 text-right">₩${Math.round(h.amount || 0).toLocaleString()}</td>
                <td class="py-2 px-2 text-center">${h.ema50_bonus ? '✓' : ''}</td>
            </tr>`;
        }).join('');
    }

    // ==================== Portfolio Overview ====================

    async loadPortfolioOverview() {
        try {
            const resp = await fetch('/api/portfolio/overview');
            const data = await resp.json();

            const inactive = document.getElementById('pfInactive');
            const panels = document.querySelectorAll('#panelPortfolio > .grid, #panelPortfolio > .bg-gray-800:not(#pfInactive)');

            if (!data.strategies || data.strategies.length === 0) {
                inactive.classList.remove('hidden');
                panels.forEach(el => el.classList.add('hidden'));
                return;
            }

            inactive.classList.add('hidden');
            panels.forEach(el => el.classList.remove('hidden'));

            // Total Summary
            document.getElementById('pfTotalValue').textContent = `₩${Math.round(data.total_value || 0).toLocaleString()}`;
            const pnl = data.total_pnl || 0;
            const pct = data.total_pct || 0;
            const pnlEl = document.getElementById('pfTotalPnL');
            pnlEl.textContent = `${pnl >= 0 ? '+' : ''}₩${Math.round(pnl).toLocaleString()} (${pct >= 0 ? '+' : ''}${pct.toFixed(1)}%)`;
            pnlEl.className = `text-sm mt-1 ${pnl >= 0 ? 'text-green-400' : 'text-red-400'}`;

            document.getElementById('pfTotalCost').textContent = `₩${Math.round(data.total_cost || 0).toLocaleString()}`;
            const activeCount = data.strategies.filter(s => s.active).length;
            document.getElementById('pfStrategies').textContent = `${activeCount} strategies active`;

            // FIRE scenarios
            const fire = data.fire || {};
            const scenarios = fire.scenarios || [];

            // Top cards: 현재 실적 기반 (첫 번째 시나리오)
            const actual = scenarios[0] || {};
            const yr6 = actual.years_to_6pct >= 50 ? '50+' : actual.fire_year_6pct;
            const yr4 = actual.years_to_4pct >= 50 ? '50+' : actual.fire_year_4pct;
            const retLabel = actual.annual_return !== undefined ? `현재 실적 연 ${actual.annual_return.toFixed(1)}%` : '-';
            document.getElementById('pfFireYear6').textContent = yr6 || '-';
            document.getElementById('pfFireYears6').textContent = actual.years_to_6pct ? `${actual.years_to_6pct}년 · ${retLabel}` : '-';
            document.getElementById('pfFireYear4').textContent = yr4 || '-';
            document.getElementById('pfFireYears4').textContent = actual.years_to_4pct ? `${actual.years_to_4pct}년 · ${retLabel}` : '-';

            // 수익률 음수면 빨간색
            if (actual.annual_return < 0) {
                document.getElementById('pfFireYear6').className = 'text-2xl font-bold text-red-400';
                document.getElementById('pfFireYear4').className = 'text-2xl font-bold text-red-400';
            } else {
                document.getElementById('pfFireYear6').className = 'text-2xl font-bold text-yellow-400';
                document.getElementById('pfFireYear4').className = 'text-2xl font-bold text-orange-400';
            }

            // FIRE Progress Bar
            const target6 = fire.target_assets_6pct || 1;
            const progress = Math.min(100, ((data.total_value || 0) / target6) * 100);
            document.getElementById('pfFireBar').style.width = `${progress}%`;
            document.getElementById('pfFirePct').textContent = `${progress.toFixed(1)}%`;
            document.getElementById('pfFireTarget').textContent = `목표 (6%): ₩${Math.round(target6).toLocaleString()}`;

            // FIRE Parameters
            document.getElementById('pfMonthlyInvest').textContent = `₩${Math.round(fire.monthly_investment || 0).toLocaleString()}`;
            document.getElementById('pfTargetMonthly').textContent = `₩${Math.round(fire.target_monthly || 0).toLocaleString()}/월`;

            // Scenario table
            const tbody = document.getElementById('pfScenarioBody');
            if (tbody) {
                tbody.innerHTML = scenarios.map((sc, i) => {
                    const isActual = i === 0;
                    const rowClass = isActual ? 'bg-gray-700/30 font-bold' : '';
                    const labelColor = isActual
                        ? (sc.annual_return < 0 ? 'text-red-400' : 'text-green-400')
                        : (sc.annual_return <= 10 ? 'text-blue-400' : 'text-yellow-400');
                    const y6 = sc.years_to_6pct >= 50 ? '50+년' : `${sc.years_to_6pct}년 (${sc.fire_year_6pct})`;
                    const y4 = sc.years_to_4pct >= 50 ? '50+년' : `${sc.years_to_4pct}년 (${sc.fire_year_4pct})`;
                    const marker = isActual ? ' ◀' : '';
                    return `<tr class="border-b border-gray-700/50 ${rowClass}">
                        <td class="py-2 px-1 ${labelColor}">${sc.label}${marker}</td>
                        <td class="py-2 px-1 text-center text-yellow-400">${y6}</td>
                        <td class="py-2 px-1 text-center text-orange-400">${y4}</td>
                    </tr>`;
                }).join('');
            }
            // Strategy Table
            this.renderPortfolioStrategies(data.strategies);

            // Allocation Chart
            this.renderAllocationChart(data.strategies, data.total_value || 0);

            // Growth Projection
            this.renderGrowthProjection(data.projection || []);

        } catch (e) {
            console.error('Portfolio overview error:', e);
        }
    }

    renderPortfolioStrategies(strategies) {
        const tbody = document.getElementById('pfStrategyTable');
        if (!strategies || !strategies.length) {
            tbody.innerHTML = '<tr><td colspan="7" class="text-center text-gray-500 py-4">No strategies</td></tr>';
            return;
        }
        tbody.innerHTML = strategies.map(s => {
            const statusClass = s.active ? 'text-green-400' : 'text-gray-500';
            const statusText = s.active ? 'Active' : 'Inactive';
            const pnlClass = (s.pnl || 0) >= 0 ? 'text-green-400' : 'text-red-400';
            const fmt = v => `₩${Math.round(v || 0).toLocaleString()}`;
            return `<tr class="border-b border-gray-700/50 hover:bg-gray-700/30">
                <td class="py-2 px-2 font-medium">${s.name}</td>
                <td class="py-2 px-2 text-center ${statusClass}">${statusText}</td>
                <td class="py-2 px-2 text-right">${fmt(s.invested)}</td>
                <td class="py-2 px-2 text-right">${fmt(s.value)}</td>
                <td class="py-2 px-2 text-right ${pnlClass}">${(s.pnl || 0) >= 0 ? '+' : ''}${fmt(s.pnl)}</td>
                <td class="py-2 px-2 text-right ${pnlClass}">${(s.pnl_pct || 0) >= 0 ? '+' : ''}${(s.pnl_pct || 0).toFixed(1)}%</td>
                <td class="py-2 px-2 text-gray-400 text-xs">${s.extra_info || '-'}</td>
            </tr>`;
        }).join('');
    }

    renderAllocationChart(strategies, totalValue) {
        const container = document.getElementById('pfAllocationChart');
        if (!strategies.length || totalValue <= 0) {
            container.innerHTML = '<p class="text-gray-500 text-sm">No data</p>';
            return;
        }

        const colors = {
            'dca': 'bg-blue-500',
            'scalp': 'bg-purple-500',
            'kr-dca': 'bg-green-500',
            'us-stock': 'bg-yellow-500',
            'kr-stock': 'bg-red-500',
        };

        container.innerHTML = strategies.filter(s => s.value > 0).map(s => {
            const pct = ((s.value || 0) / totalValue * 100);
            const color = colors[s.type] || 'bg-gray-500';
            return `<div>
                <div class="flex justify-between text-sm mb-1">
                    <span class="text-gray-300">${s.name}</span>
                    <span class="text-gray-400">₩${Math.round(s.value).toLocaleString()} (${pct.toFixed(1)}%)</span>
                </div>
                <div class="h-3 rounded-full bg-gray-700 overflow-hidden">
                    <div class="h-3 rounded-full ${color} transition-all" style="width:${pct}%"></div>
                </div>
            </div>`;
        }).join('');
    }

    renderGrowthProjection(projection) {
        const container = document.getElementById('pfGrowthChart');
        if (!projection || !projection.length) {
            container.innerHTML = '<p class="text-gray-500 text-sm">No projection data</p>';
            return;
        }

        const maxVal = Math.max(...projection.map(p => p.total_assets));

        container.innerHTML = projection.map(p => {
            const height = maxVal > 0 ? (p.total_assets / maxVal * 100) : 0;
            const invHeight = maxVal > 0 ? (p.invested / maxVal * 100) : 0;
            const isQuarter = p.month % 3 === 0;
            return `<div class="flex flex-col items-center flex-1 min-w-0" title="M${p.month}: ₩${Math.round(p.total_assets).toLocaleString()}">
                <div class="w-full relative" style="height:${height}%">
                    <div class="absolute bottom-0 w-full bg-blue-600/30 rounded-t" style="height:${invHeight / height * 100}%"></div>
                    <div class="absolute bottom-0 w-full bg-blue-500 rounded-t" style="height:100%;opacity:0.7"></div>
                </div>
                ${isQuarter ? `<div class="text-gray-500 text-xs mt-1">M${p.month}</div>` : ''}
            </div>`;
        }).join('');

        // Milestone values
        const fmt = v => `₩${Math.round(v).toLocaleString()}`;
        if (projection.length >= 6) document.getElementById('pfProj6m').textContent = fmt(projection[5].total_assets);
        if (projection.length >= 12) document.getElementById('pfProj12m').textContent = fmt(projection[11].total_assets);
        if (projection.length >= 18) document.getElementById('pfProj18m').textContent = fmt(projection[17].total_assets);
        if (projection.length >= 24) document.getElementById('pfProj24m').textContent = fmt(projection[23].total_assets);
    }

    // ==================== Collector Methods ====================

    async loadCollectorStatus() {
        try {
            const resp = await fetch('/api/collector/status');
            const data = await resp.json();

            const inactive = document.getElementById('collectorInactive');
            const panels = document.querySelectorAll('#panelCollector > .grid, #panelCollector > .bg-gray-800:not(#collectorInactive)');

            if (!data.active) {
                inactive.classList.remove('hidden');
                panels.forEach(el => el.classList.add('hidden'));
                return;
            }

            inactive.classList.add('hidden');
            panels.forEach(el => el.classList.remove('hidden'));

            // DB Size
            const dbMB = (data.db_size / (1024 * 1024)).toFixed(1);
            document.getElementById('colDbSize').textContent = `${dbMB} MB`;

            // Total counts
            const candles = data.candles || [];
            const orderbook = data.orderbook || [];
            const totalCandles = candles.reduce((s, c) => s + c.count, 0);
            const totalOrderbook = orderbook.reduce((s, o) => s + o.count, 0);
            document.getElementById('colTotalCandles').textContent = totalCandles.toLocaleString();
            document.getElementById('colTotalOrderbook').textContent = totalOrderbook.toLocaleString();
            document.getElementById('colTotalSignals').textContent = (data.signals?.count || 0).toLocaleString();

            // Market summary table
            const tbody = document.getElementById('colMarketTable');
            const now = Date.now() / 1000;
            let rows = '';

            for (const c of candles) {
                const age = now - c.latest;
                const ageStr = age < 120 ? `${Math.floor(age)}s ago` : age < 7200 ? `${Math.floor(age/60)}m ago` : `${Math.floor(age/3600)}h ago`;
                const status = age < 300 ? '<span class="text-green-400">Active</span>' : age < 3600 ? '<span class="text-yellow-400">Delayed</span>' : '<span class="text-red-400">Stale</span>';
                rows += `<tr class="border-b border-gray-700/50">
                    <td class="py-2 px-2 text-gray-300">Candles</td>
                    <td class="py-2 px-2 text-white">${c.market}</td>
                    <td class="py-2 px-2 text-right text-gray-300">${c.count.toLocaleString()}</td>
                    <td class="py-2 px-2 text-right text-gray-400">${ageStr}</td>
                    <td class="py-2 px-2 text-center">${status}</td>
                </tr>`;
            }

            for (const o of orderbook) {
                const age = now - o.latest;
                const ageStr = age < 120 ? `${Math.floor(age)}s ago` : age < 7200 ? `${Math.floor(age/60)}m ago` : `${Math.floor(age/3600)}h ago`;
                const status = age < 300 ? '<span class="text-green-400">Active</span>' : age < 3600 ? '<span class="text-yellow-400">Delayed</span>' : '<span class="text-red-400">Stale</span>';
                rows += `<tr class="border-b border-gray-700/50">
                    <td class="py-2 px-2 text-gray-300">Orderbook</td>
                    <td class="py-2 px-2 text-white">${o.market}</td>
                    <td class="py-2 px-2 text-right text-gray-300">${o.count.toLocaleString()}</td>
                    <td class="py-2 px-2 text-right text-gray-400">${ageStr}</td>
                    <td class="py-2 px-2 text-center">${status}</td>
                </tr>`;
            }

            if (data.signals?.count > 0) {
                const age = now - data.signals.latest;
                const ageStr = age < 120 ? `${Math.floor(age)}s ago` : age < 7200 ? `${Math.floor(age/60)}m ago` : `${Math.floor(age/3600)}h ago`;
                const status = age < 300 ? '<span class="text-green-400">Active</span>' : age < 3600 ? '<span class="text-yellow-400">Delayed</span>' : '<span class="text-red-400">Stale</span>';
                rows += `<tr class="border-b border-gray-700/50">
                    <td class="py-2 px-2 text-gray-300">Crypto Signals</td>
                    <td class="py-2 px-2 text-white">binance_futures</td>
                    <td class="py-2 px-2 text-right text-gray-300">${data.signals.count.toLocaleString()}</td>
                    <td class="py-2 px-2 text-right text-gray-400">${ageStr}</td>
                    <td class="py-2 px-2 text-center">${status}</td>
                </tr>`;
            }

            tbody.innerHTML = rows || '<tr><td colspan="5" class="text-center text-gray-500 py-4">No data</td></tr>';

            // Today's collection
            const todayBody = document.getElementById('colTodayTable');
            const today = data.today || [];
            if (today.length === 0) {
                todayBody.innerHTML = '<tr><td colspan="3" class="text-center text-gray-500 py-4">No data collected today</td></tr>';
            } else {
                todayBody.innerHTML = today.map(t => `<tr class="border-b border-gray-700/50">
                    <td class="py-1 px-2 text-gray-400">${t.market}</td>
                    <td class="py-1 px-2 text-white">${t.symbol}</td>
                    <td class="py-1 px-2 text-right text-green-400">${t.count.toLocaleString()}</td>
                </tr>`).join('');
            }
        } catch (e) {
            console.error('Collector status error:', e);
        }
    }
}

// Initialize app when DOM is ready
document.addEventListener('DOMContentLoaded', () => {
    window.app = new TravelerApp();
});
