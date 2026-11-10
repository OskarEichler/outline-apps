// Copyright 2018 The Outline Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import {LitElement, html, css, PropertyValues, nothing} from 'lit';
import {customElement, property, state} from 'lit/decorators.js';
import uuidv4 from 'uuidv4';

import {
  MeasurementServer,
  ServerMeasurement,
} from '../../model/server_measurement';

type Results = {latency?: ServerMeasurement; speed?: ServerMeasurement};

@customElement('server-diagnostics-view')
export class ServerDiagnosticsView extends LitElement {
  @property({attribute: false}) listServers?: () => MeasurementServer[];
  @property({attribute: false}) measure?: (
    server: string,
    speed: boolean,
    id: string
  ) => Promise<ServerMeasurement>;
  @property({attribute: false}) cancel?: (id: string) => Promise<void>;
  @property({attribute: false}) connect?: (server: string) => Promise<void>;
  @property({attribute: false}) localize: (key: string) => string = key => key;
  @property({type: String}) page = '';
  @state() private servers: MeasurementServer[] = [];
  @state() private results: Record<string, Results> = {};
  @state() private busy = false;
  @state() private message = '';
  @state() private runningServer = '';
  @state() private speedRunning = false;
  private stopped = false;
  private token = '';

  static styles = css`
    :host {
      display: block;
      box-sizing: border-box;
      padding: 24px;
      color: var(--outline-text-color);
      background: var(--outline-background);
    }
    .content {
      max-width: 600px;
      margin: 0 auto;
    }
    p {
      line-height: 1.5;
    }
    .toolbar,
    .actions {
      display: flex;
      gap: 12px;
      flex-wrap: wrap;
      align-items: center;
    }
    .toolbar {
      margin: 20px 0;
    }
    article {
      background: var(--outline-card-background);
      border: 1px solid var(--outline-hairline);
      border-radius: 12px;
      padding: 16px;
      margin: 16px 0;
    }
    h2 {
      font: 600 1.1rem var(--outline-font-family);
      margin: 0;
      overflow-wrap: anywhere;
    }
    .badge {
      display: inline-block;
      margin-top: 8px;
      color: var(--outline-primary);
      font-size: 0.85rem;
    }
    dl {
      display: grid;
      grid-template-columns: repeat(3, minmax(0, 1fr));
      gap: 8px;
      margin: 20px 0;
    }
    dl div {
      min-width: 0;
    }
    dt {
      color: var(--outline-label-color);
      font-size: 0.8rem;
      margin-bottom: 6px;
    }
    dd {
      margin: 0;
      font-size: 1rem;
      font-variant-numeric: tabular-nums;
      overflow-wrap: anywhere;
    }
    small {
      display: block;
      line-height: 1.5;
      margin-top: 12px;
      color: var(--outline-label-color);
    }
    .error {
      color: var(--outline-error);
    }
    [role='status'] {
      min-height: 24px;
    }
    @media (max-width: 340px) {
      :host {
        padding: 16px;
      }
      dl {
        grid-template-columns: 1fr;
      }
      dl div {
        display: flex;
        gap: 12px;
      }
    }
  `;

  protected updated(changed: PropertyValues) {
    if (
      (changed.has('page') || changed.has('listServers')) &&
      this.page === 'server-diagnostics' &&
      this.listServers
    ) {
      this.servers = this.listServers();
      if (!this.busy) void this.checkLatencies();
    } else if (
      changed.has('page') &&
      this.page !== 'server-diagnostics' &&
      this.busy
    ) {
      void this.stop();
    }
  }

  disconnectedCallback() {
    if (this.busy) void this.stop();
    super.disconnectedCallback();
  }

  private get best(): MeasurementServer | undefined {
    return this.servers
      .filter(server =>
        Number.isFinite(this.results[server.id]?.latency?.latencyMs)
      )
      .sort(
        (a, b) =>
          this.results[a.id].latency!.latencyMs! -
          this.results[b.id].latency!.latencyMs!
      )[0];
  }

  private async test(server: MeasurementServer, speed: boolean) {
    if (this.stopped || !this.measure) return;
    this.token = uuidv4();
    this.runningServer = server.id;
    this.speedRunning = speed;
    this.message = `${this.localize(speed ? 'server-tests-running-speed' : 'server-tests-running-latency')} ${server.name}`;
    let result: ServerMeasurement;
    try {
      result = await this.measure(server.id, speed, this.token);
    } catch {
      result = {ok: false, error: 'unavailable'};
    }
    if (!this.stopped) {
      this.results = {
        ...this.results,
        [server.id]: {
          ...this.results[server.id],
          latency: result,
          ...(speed ? {speed: result} : {}),
        },
      };
    }
    this.token = '';
  }

  private async checkLatencies() {
    if (this.busy || !this.measure) return;
    this.busy = true;
    this.stopped = false;
    try {
      for (const server of this.servers) {
        if (this.stopped) break;
        await this.test(server, false);
      }
      this.message = this.localize(
        this.stopped ? 'server-tests-cancelled' : 'server-tests-complete'
      );
    } finally {
      this.busy = false;
      this.runningServer = '';
    }
  }

  private async speedTest(server: MeasurementServer) {
    if (this.busy || !this.measure) return;
    this.busy = true;
    this.stopped = false;
    try {
      await this.test(server, true);
      this.message = this.localize(
        this.stopped ? 'server-tests-cancelled' : 'server-tests-complete'
      );
    } finally {
      this.busy = false;
      this.runningServer = '';
    }
  }

  private async stop() {
    this.stopped = true;
    this.message = this.localize('server-tests-cancelling');
    if (this.token) {
      try {
        await this.cancel?.(this.token);
      } catch {
        /* The bounded test also expires on its own. */
      }
    }
  }

  private format(value?: number, unit = 'Mbps') {
    return Number.isFinite(value) ? `${value!.toFixed(1)} ${unit}` : '—';
  }

  private error(result?: ServerMeasurement) {
    if (!result || result.ok || !result.error) return nothing;
    const key = [
      'busy',
      'cancelled',
      'connection_changed',
      'connection_changing',
      'reconnect_required',
      'download_failed',
      'upload_failed',
    ].includes(result.error)
      ? `server-tests-error-${result.error}`
      : 'server-tests-error-unavailable';
    return html`<p class="error">${this.localize(key)}</p>`;
  }

  render() {
    const best = this.best;
    return html`<div class="content">
      <p>${this.localize('server-tests-description')}</p>
      <p>${this.localize('server-tests-bandwidth')}</p>
      <div class="toolbar">
        <md-filled-button
          ?disabled=${this.busy || !this.servers.length}
          @click=${this.checkLatencies}
          >${this.localize('server-tests-refresh')}</md-filled-button
        >
        ${this.busy
          ? html`<md-text-button @click=${this.stop}
              >${this.localize('server-tests-cancel')}</md-text-button
            >`
          : nothing}
      </div>
      ${best
        ? html`<p>
            ${this.localize('server-tests-lowest')}:
            <strong>${best.name}</strong>
            <md-text-button
              ?disabled=${this.busy}
              aria-label=${`${this.localize('server-tests-connect')} ${best.name}`}
              @click=${() => this.connect?.(best.id)}
              >${this.localize('server-tests-connect')}</md-text-button
            >
          </p>`
        : nothing}
      <p role="status" aria-live="polite">${this.message}</p>
      ${!this.servers.length
        ? html`<p>${this.localize('server-tests-empty')}</p>`
        : nothing}
      ${this.servers.map(server => {
        const row = this.results[server.id];
        const running = this.runningServer === server.id;
        return html`<article
          aria-label=${server.name}
          aria-busy=${running ? 'true' : 'false'}
        >
          <h2>${server.name}</h2>
          ${best?.id === server.id
            ? html`<span class="badge"
                >${this.localize('server-tests-lowest')}</span
              >`
            : nothing}
          <dl>
            <div>
              <dt>${this.localize('server-tests-latency')}</dt>
              <dd>${this.format(row?.latency?.latencyMs, 'ms')}</dd>
            </div>
            <div>
              <dt>${this.localize('server-tests-download')}</dt>
              <dd>${this.format(row?.speed?.downloadMbps)}</dd>
            </div>
            <div>
              <dt>${this.localize('server-tests-upload')}</dt>
              <dd>${this.format(row?.speed?.uploadMbps)}</dd>
            </div>
          </dl>
          <div class="actions">
            <md-outlined-button
              ?disabled=${this.busy}
              aria-label=${`${this.localize('server-tests-speed')} ${server.name}`}
              @click=${() => this.speedTest(server)}
              >${running && this.speedRunning
                ? this.localize('server-tests-testing')
                : this.localize('server-tests-speed')}</md-outlined-button
            >
            <md-text-button
              ?disabled=${this.busy}
              aria-label=${`${this.localize('server-tests-connect')} ${server.name}`}
              @click=${() => this.connect?.(server.id)}
              >${this.localize('server-tests-connect')}</md-text-button
            >
          </div>
          ${row?.speed?.measuredAt
            ? html`<small
                >${this.localize('server-tests-speed-tested')}
                ${new Date(row.speed.measuredAt).toLocaleString()}</small
              >`
            : nothing}
          ${this.error(row?.latency)}
        </article>`;
      })}
    </div>`;
  }
}
