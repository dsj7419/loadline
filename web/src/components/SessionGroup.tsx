import type { ReadonlySignal } from '@preact/signals';
import type { Group } from '../model/group';
import { SessionRow } from './SessionRow';

type SessionGroupProps = {
  readonly group: Group;
  readonly now: ReadonlySignal<Date>;
  readonly onOpen: (id: string) => void;
};

/** The sessions under one heading, such as Needs you. */
export function SessionGroup({ group, now, onOpen }: SessionGroupProps) {
  return (
    <section class={`group group-${group.key}`}>
      <div class="group-head">
        <h3>{group.label}</h3>
        <span class="group-count num">{group.sessions.length}</span>
      </div>
      <ul class="rows">
        {group.sessions.map((session) => (
          <SessionRow key={session.id} session={session} now={now} onOpen={onOpen} />
        ))}
      </ul>
    </section>
  );
}
