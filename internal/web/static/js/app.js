// Traveler Dashboard - Main Application Logic

class TravelerApp {
    constructor() {
        this.signals = [];
        this.excluded = new Set();
        this.capital = 50000;
        this.currentSignal = null;
        this.settings = {
            capital: 50000,
            riskPct: 1,
            maxPositions: 5
        };

        this.loadSettings();
        this.initEventListeners();
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
        const activeSignals = this.signals.filter(s =>
            !this.excluded.has(s.stock.symbol || s.stock.Symbol)
        );

        // Recalculate position sizing
        let totalInvest = 0;
        let totalRisk = 0;

        if (activeSignals.length > 0) {
            const allocationPerPosition = this.capital / activeSignals.length;
            const riskPerPosition = this.capital * (this.settings.riskPct / 100) / activeSignals.length;

            activeSignals.forEach(signal => {
                if (signal.guide) {
                    const g = signal.guide;
                    const entryPrice = g.entry_price || g.EntryPrice || 0;
                    const stopLoss = g.stop_loss || g.StopLoss || 0;
                    const riskPerShare = entryPrice - stopLoss;

                    if (riskPerShare > 0) {
                        const sharesByRisk = Math.floor(riskPerPosition / riskPerShare);
                        const sharesByAllocation = Math.floor(allocationPerPosition / entryPrice);
                        let shares = Math.min(sharesByRisk, sharesByAllocation);
                        if (shares < 1) shares = 1;

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
        document.getElementById('totalCapital').textContent = this.formatUSD(this.capital);
        document.getElementById('totalInvested').textContent = this.formatUSD(totalInvest);
        document.getElementById('totalRisk').textContent = `${this.formatUSD(totalRisk)} (${(totalRisk / this.capital * 100).toFixed(2)}%)`;
        document.getElementById('cashRemaining').textContent = this.formatUSD(this.capital - totalInvest);

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

            const row = document.createElement('tr');
            row.className = 'hover:bg-gray-750 cursor-pointer';
            row.innerHTML = `
                <td class="px-4 py-3 text-gray-400">${index + 1}</td>
                <td class="px-4 py-3 font-semibold text-blue-400">${symbol}</td>
                <td class="px-4 py-3">$${entryPrice.toFixed(2)}</td>
                <td class="px-4 py-3">${shares}</td>
                <td class="px-4 py-3">${this.formatUSD(investAmount)}</td>
                <td class="px-4 py-3">${allocationPct.toFixed(1)}%</td>
                <td class="px-4 py-3 text-red-400">${this.formatUSD(riskAmount)}</td>
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
        document.getElementById('modalEntry').textContent = `$${(guide.entry_price || guide.EntryPrice || 0).toFixed(2)}`;
        document.getElementById('modalStopLoss').textContent = `$${(guide.stop_loss || guide.StopLoss || 0).toFixed(2)} (${(guide.stop_loss_pct || guide.StopLossPct || 0).toFixed(1)}%)`;
        document.getElementById('modalTarget1').textContent = `$${(guide.target_1 || guide.Target1 || 0).toFixed(2)} (+${(guide.target_1_pct || guide.Target1Pct || 0).toFixed(1)}%)`;
        document.getElementById('modalTarget2').textContent = `$${(guide.target_2 || guide.Target2 || 0).toFixed(2)} (+${(guide.target_2_pct || guide.Target2Pct || 0).toFixed(1)}%)`;
        document.getElementById('modalShares').value = guide.position_size || guide.PositionSize || 0;
        document.getElementById('modalReason').textContent = signal.reason || 'N/A';

        this.updateModalInvestment(guide.position_size || guide.PositionSize || 0);

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

        document.getElementById('modalInvestment').textContent = this.formatUSD(investment);
        document.getElementById('modalRiskAmount').textContent = this.formatUSD(riskAmount);
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
        const capital = parseFloat(document.getElementById('capitalInput').value) || 50000;
        const universe = document.getElementById('universeSelect').value;

        this.showLoading(true);

        try {
            const response = await fetch(`/api/scan?capital=${capital}&universe=${universe}`);
            if (!response.ok) {
                throw new Error('Scan failed: ' + response.statusText);
            }

            const data = await response.json();
            this.capital = capital;
            this.signals = (data.signals || data.Signals || []).map(s => this.normalizeSignal(s));
            this.excluded.clear();
            this.recalculate();
            this.showUI();
        } catch (e) {
            console.error('Scan error:', e);
            alert('Scan failed: ' + e.message);
        } finally {
            this.showLoading(false);
        }
    }

    showLoading(show) {
        document.getElementById('loading').classList.toggle('hidden', !show);
    }

    formatUSD(amount) {
        if (amount >= 1000000) {
            return `$${(amount / 1000000).toFixed(2)}M`;
        } else if (amount >= 1000) {
            return `$${(amount / 1000).toFixed(1)}K`;
        }
        return `$${amount.toFixed(2)}`;
    }
}

// Initialize app when DOM is ready
document.addEventListener('DOMContentLoaded', () => {
    window.app = new TravelerApp();
});
