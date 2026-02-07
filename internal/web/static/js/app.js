// Traveler Dashboard - Main Application Logic

class TravelerApp {
    constructor() {
        this.signals = [];
        this.excluded = new Set();
        this.capital = 50000;
        this.currentSignal = null;
        this.market = 'us'; // 'us' or 'kr'
        this.settings = {
            capital: 50000,
            riskPct: 1,
            maxPositions: 5
        };
        this.positionsRefreshTimer = null;
        this.activeTab = 'scanner';

        this.loadSettings();
        this.initEventListeners();
        this.initTabs();
        this.initMarketToggle();

        // Auto-load last scan result or attach to running scan
        this.loadLastResult();
    }

    loadSettings() {
        const saved = localStorage.getItem('traveler_settings');
        if (saved) {
            try {
                this.settings = JSON.parse(saved);
                this.capital = this.settings.capital;
            } catch (e) {
                console.error('Failed to load settings:', e);
            }
        }
    }

    saveSettings() {
        localStorage.setItem('traveler_settings', JSON.stringify(this.settings));
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
        if (market === 'kr') {
            if (capitalLabel) capitalLabel.textContent = 'Capital:';
            if (capitalInput && parseFloat(capitalInput.value) < 10000) {
                capitalInput.value = 1000000; // Default ₩1,000,000
                this.capital = 1000000;
            }
        } else {
            if (capitalLabel) capitalLabel.textContent = 'Capital ($):';
            if (capitalInput && parseFloat(capitalInput.value) >= 10000) {
                capitalInput.value = 200; // Default $200
                this.capital = 200;
            }
        }

        // Reload data for active tab
        if (this.activeTab === 'positions') {
            this.loadPositionsData();
        } else if (this.activeTab === 'scanner') {
            this.loadLastResult(true);
        }
    }

    isKR() {
        return this.market === 'kr';
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

        // Load positions data when switching to positions tab
        if (tab === 'positions') {
            this.loadPositionsData();
            this.startPositionsRefresh();
        } else {
            this.stopPositionsRefresh();
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
            const mq = this.isKR() ? '?market=kr' : '';
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

            // Hide scanner-specific buttons
            document.getElementById('excludeBtn').classList.add('hidden');
            document.getElementById('applySharesBtn').classList.add('hidden');

            document.getElementById('stockModal').classList.remove('hidden');
        } catch (e) {
            console.error('Failed to load chart:', e);
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

        // Settings modal
        document.getElementById('settingsBtn').addEventListener('click', () => this.showSettingsModal());
        document.getElementById('closeSettings').addEventListener('click', () => this.hideSettingsModal());
        document.getElementById('saveSettings').addEventListener('click', () => this.applySettings());

        // Stock modal
        document.getElementById('closeModal').addEventListener('click', () => this.hideStockModal());
        document.getElementById('excludeBtn').addEventListener('click', () => this.excludeCurrentStock());
        document.getElementById('applySharesBtn').addEventListener('click', () => this.applyShares());
        document.getElementById('modalShares').addEventListener('change', (e) => this.updateModalInvestment(e.target.value));

        // Close modals on escape
        document.addEventListener('keydown', (e) => {
            if (e.key === 'Escape') {
                this.hideStockModal();
                this.hideSettingsModal();
            }
        });

        // Click outside modal to close
        document.getElementById('stockModal').addEventListener('click', (e) => {
            if (e.target.id === 'stockModal') this.hideStockModal();
        });
        document.getElementById('settingsModal').addEventListener('click', (e) => {
            if (e.target.id === 'settingsModal') this.hideSettingsModal();
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
            candles: signal.candles || signal.Candles || []
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
    }

    recalculate() {
        this.capital = parseFloat(document.getElementById('capitalInput').value) || this.capital;
        const minCap = this.isKR() ? 100000 : 100;
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
            const riskPct = this.isKR() ? 1.5 : (this.settings.riskPct || 1);
            const maxPosPct = this.isKR() ? 0.25 : 0.20;
            const riskBudget = this.capital * (riskPct / 100); // per trade, not per position
            const maxPositionValue = this.capital * maxPosPct;

            activeSignals.forEach(signal => {
                if (signal.guide) {
                    const g = signal.guide;
                    const entryPrice = g.entry_price || g.EntryPrice || 0;
                    const stopLoss = g.stop_loss || g.StopLoss || 0;
                    const riskPerShare = entryPrice - stopLoss;

                    if (riskPerShare > 0 && entryPrice > 0) {
                        const sharesByRisk = Math.floor(riskBudget / riskPerShare);
                        const sharesByAllocation = Math.floor(maxPositionValue / entryPrice);
                        let shares = Math.min(sharesByRisk, sharesByAllocation);
                        if (shares < 1) shares = 1; // minimum 1 share

                        // Cap at maxPositionValue
                        if (shares * entryPrice > maxPositionValue) {
                            shares = Math.floor(maxPositionValue / entryPrice);
                        }
                        if (shares < 1) shares = 0;

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
                'mean-reversion': 'bg-teal-600'
            };
            const stratBg = stratColors[strategyName] || 'bg-gray-600';

            row.innerHTML = `
                <td class="px-4 py-3 text-gray-400">${index + 1}</td>
                <td class="px-4 py-3 font-semibold text-blue-400">${displaySym}</td>
                <td class="px-4 py-3"><span class="${stratBg} px-2 py-0.5 rounded text-xs font-medium">${strategyName}</span></td>
                <td class="px-4 py-3">${this.formatPrice(entryPrice)}</td>
                <td class="px-4 py-3">${shares}</td>
                <td class="px-4 py-3">${this.formatMoney(investAmount)}</td>
                <td class="px-4 py-3">${allocationPct.toFixed(1)}%</td>
                <td class="px-4 py-3 text-red-400">${this.formatMoney(riskAmount)}</td>
                <td class="px-4 py-3 text-green-400">${probability.toFixed(0)}%</td>
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

    showSettingsModal() {
        document.getElementById('settingsCapital').value = this.settings.capital;
        document.getElementById('settingsRisk').value = this.settings.riskPct;
        document.getElementById('settingsMaxPos').value = this.settings.maxPositions;
        document.getElementById('settingsModal').classList.remove('hidden');
    }

    hideSettingsModal() {
        document.getElementById('settingsModal').classList.add('hidden');
    }

    applySettings() {
        this.settings.capital = parseFloat(document.getElementById('settingsCapital').value) || 50000;
        this.settings.riskPct = parseFloat(document.getElementById('settingsRisk').value) || 1;
        this.settings.maxPositions = parseInt(document.getElementById('settingsMaxPos').value) || 5;

        this.capital = this.settings.capital;
        document.getElementById('capitalInput').value = this.capital;

        this.saveSettings();
        this.hideSettingsModal();

        if (this.signals.length > 0) {
            this.recalculate();
        }
    }

    async runScan() {
        const capital = parseFloat(document.getElementById('capitalInput').value) || 200;
        const minCapital = this.isKR() ? 100000 : 100;
        const currLabel = this.isKR() ? '₩' : '$';
        if (capital < minCapital) {
            alert(`Minimum capital is ${currLabel}${minCapital.toLocaleString()}`);
            return;
        }

        const scanLabel = this.isKR() ? 'KR Adaptive Multi-Strategy Scan' : 'Adaptive Multi-Strategy Scan';
        this.showLoading(true, scanLabel, 'Starting...');

        try {
            // Fire-and-forget: start scan
            const mq = this.isKR() ? `&market=kr` : '';
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
            const statusMq = this.isKR() ? '?market=kr' : '';
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
            const mq = this.isKR() ? '?market=kr' : '';
            let st = await fetch('/api/scan/status' + mq).then(r => r.json());

            // On initial page load only: if current market has no result, auto-switch
            if (!noAutoSwitch && st.status === 'idle') {
                const otherMarket = this.isKR() ? 'us' : 'kr';
                const otherMq = otherMarket === 'kr' ? '?market=kr' : '';
                const otherSt = await fetch('/api/scan/status' + otherMq).then(r => r.json());
                if (otherSt.status === 'done') {
                    this.market = otherMarket;
                    document.querySelectorAll('.market-btn').forEach(btn => {
                        btn.classList.toggle('active', btn.dataset.market === otherMarket);
                    });
                    st = otherSt;
                }
            }

            if (st.status === 'done') {
                const capital = parseFloat(document.getElementById('capitalInput').value) || this.capital;
                await this.fetchScanResult(capital);
            } else if (st.status === 'running') {
                this.showLoading(true, 'Scan in progress', st.message || '');
                this._scanPoll = setInterval(async () => {
                    try {
                        const poll = await fetch('/api/scan/status' + mq).then(r => r.json());
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
                const marketLabel = this.isKR() ? 'KR' : 'US';
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
            const mq = this.isKR() ? '?market=kr' : '';
            let res = await fetch('/api/scan/result' + mq);
            if (!res.ok) {
                // No result for this market — clear UI
                this.showLoading(false);
                this.signals = [];
                this.excluded.clear();
                this.recalculate();
                this.hideUI();
                const label = this.isKR() ? 'KR' : 'US';
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
            meta.push(data.scan_time || '');
            document.getElementById('scanMeta').textContent = meta.filter(Boolean).join(' | ');
            document.getElementById('recalculateBtn').classList.remove('hidden');
        } catch (err) {
            this.showLoading(false);
            alert('Failed to load scan result: ' + err.message);
        }
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
        return this.isKR() ? this.formatKRW(amount) : this.formatUSD(amount);
    }

    formatPrice(price) {
        if (this.isKR()) {
            return `₩${Math.round(price).toLocaleString()}`;
        }
        return `$${price.toFixed(2)}`;
    }
}

// Initialize app when DOM is ready
document.addEventListener('DOMContentLoaded', () => {
    window.app = new TravelerApp();
});
