// Synthetic in-page HTTP/WS boundary for the scheduled prompt editor. No hub or model is contacted.
(() => {
  localStorage.setItem('persona.token', 'synthetic-task-token');
  localStorage.setItem('persona.language', 'en');
  const fixture = window.__taskInputFixture = {
    task: {
      id: 'job',
      title: 'Review project files',
      prompt: 'Summarize the project files.',
      repeat: 'interval',
      every_minutes: 30,
      enabled: true,
      model_revision: 0
    },
    reads: [],
    writes: [],
    refuse: false,
    taskInput: true
  };
  const executionFor = task => ({
    machine_id: task.execution_machine_id || 'box',
    launcher: task.launcher || 'claude',
    agent: task.launcher || 'claude',
    model: task.model || (task.launcher && task.launcher !== 'claude' ? 'agent-default' : 'main-model'),
    effort: task.effort || 'high',
    cwd: task.cwd || '/work/default'
  });
  const viewTask = () => ({
    ...fixture.task,
    execution: executionFor(fixture.task)
  });
  const nativeFetch = window.fetch.bind(window);
  window.fetch = async (input, init = {}) => {
    const url = new URL(typeof input === 'string' ? input : input.url, location.href);
    if (!url.pathname.startsWith('/api/v1/')) return nativeFetch(input, init);
    const path = url.pathname.slice('/api/v1'.length);
    const method = init.method || 'GET';
    fixture.reads.push({
      path,
      method,
      query: url.search
    });
    const patch = init.body ? JSON.parse(init.body) : {};
    const storedFields = ({ structured_prompt, ...fields }) =>
      structured_prompt === undefined ? { ...fields, prompt_selections: false } : { ...fields, prompt: structured_prompt, prompt_selections: true };
    let body = {};
    if (path === '/machines') body = {
      machines: [{
        id: 'box',
        name: 'Example computer',
        online: true
      }, {
        id: 'remote',
        name: 'Project computer',
        online: true
      }]
    };
    else if (path === '/sessions') body = {
      sessions: []
    };
    else if (path === '/assistant') body = {
      machine_id: 'box',
      main: '',
      conversations: [],
      tasks: [],
      memory: []
    };
    else if (path === '/assistant/tasks' && method === 'POST') {
      fixture.created = {
        ...storedFields(patch),
        id: 'new-task',
        model_revision: 0
      };
      body = {
        task: {
          ...fixture.created,
          execution: executionFor(fixture.created)
        }
      };
    } else if (path === '/assistant/tasks') body = {
      tasks: [viewTask(), ...(fixture.created ? [fixture.created] : [])]
    };
    else if (path === '/assistant/runs') body = {
      runs: []
    };
    else if (path === '/model-config/purposes/assistant') body = {
      purpose: {
        id: 'assistant',
        machine_id: 'box',
        effective: {
          provider: 'launcher.claude',
          model: 'main-model',
          effort: 'high'
        },
        override: {}
      },
      providers: []
    };
    else if (/\/machines\/[^/]+\/launchers$/.test(path)) body = {
      launchers: [{
        name: 'claude',
        display_name: 'Assistant',
        agent: 'claude'
      }, {
        name: 'codex',
        display_name: 'agents',
        agent: 'codex'
      }]
    };
    else if (path.startsWith('/model-config/providers/')) body = {
      agent: path.includes('codex') ? 'codex' : 'claude',
      models: [{
        id: 'agent-default',
        default: true,
        default_effort: 'high',
        efforts: ['low', 'high']
      }, {
        id: 'test-model',
        name: 'Example model',
        efforts: ['low', 'high']
      }]
    };
    else if (path === '/assistant/tasks/input-context') {
      const task = {
        ...(patch.id === fixture.task.id ? fixture.task : patch.id === fixture.created?.id ? fixture.created : {}),
        ...patch
      };
      body = { execution: executionFor(task), task_input: fixture.taskInput };
      if (!fixture.taskInput) body.input_error = 'Update Persona on this computer to select files and skills.';
      if (patch.catalog && fixture.taskInput) body.catalog = {
        catalog_version: 1,
        complete: true,
        skills: [{
          id: 'project-review',
          name: 'Project review',
          source: 'project · .',
          description: 'Review selected project files'
        }]
      };
    } else if (/\/machines\/[^/]+\/fs$/.test(path)) body = {
      path: url.searchParams.get('path'),
      entries: [{
        name: 'notes.txt',
        dir: false,
        size: 20
      }, {
        name: 'src',
        dir: true
      }],
      truncated: false
    };
    else if (path === '/assistant/tasks/job' && method === 'PUT') {
      fixture.writes.push(patch);
      if (fixture.refuse) return new Response(JSON.stringify({
        error: 'Selected reference is unavailable'
      }), {
        status: 400,
        headers: {
          'Content-Type': 'application/json'
        }
      });
      fixture.task = {
        ...fixture.task,
        ...storedFields(patch),
        model_revision: (fixture.task.model_revision || 0) + 1
      };
      body = {
        task: viewTask()
      };
    } else if (path === '/extensions/-/prompts') body = {
      prompts: [{
        extension: 'example',
        extension_name: 'Example',
        id: 'review',
        title: 'Review',
        text: 'Review the selected files.'
      }]
    };
    else if (path === '/extensions/ui') body = {
      slots: []
    };
    else if (path === '/extensions') body = {
      extensions: []
    };
    else if (path === '/managers') body = {
      managers: []
    };
    else if (!['GET', 'HEAD'].includes(method)) throw new Error(`Unexpected fixture write ${method} ${path}`);
    return new Response(JSON.stringify(body), {
      status: 200,
      headers: {
        'Content-Type': 'application/json'
      }
    });
  };
  window.WebSocket = class extends EventTarget {
    static CONNECTING = 0;
    static OPEN = 1;
    static CLOSING = 2;
    static CLOSED = 3;
    readyState = 0;
    constructor() {
      super();
      setTimeout(() => {
        this.readyState = 1;
        this.onopen?.(new Event('open'));
        this.dispatchEvent(new Event('open'));
        this.deliver({
          type: 'hello',
          server_version: 'synthetic',
          features: ['task_execution', 'task_input', 'task_cron', 'extensions']
        });
      }, 20);
    }
    deliver(value) {
      const event = new MessageEvent('message', {
        data: JSON.stringify(value)
      });
      this.onmessage?.(event);
      this.dispatchEvent(event);
    }
    send(raw) {
      if (JSON.parse(raw).type === 'app_ping') this.deliver({
        type: 'pong'
      });
    }
    close() {
      this.readyState = 3;
    }
  };
})();
