import fs from 'node:fs';
import path from 'node:path';

export class FileService {
  constructor(root) {
    this.root = fs.realpathSync(root);
  }
  safePath(relative) {
    const decoded = decodeURIComponent(relative);
    if (decoded === '') return this.root;
    if (path.isAbsolute(decoded)) throw new Error('unsafe path');
    const target = path.resolve(this.root, decoded);
    if (target !== this.root && !target.startsWith(`${this.root}${path.sep}`)) {
      throw new Error('unsafe path');
    }
    const parent = path.dirname(target);
    if (
      fs.existsSync(parent) &&
      parent !== this.root &&
      !fs.realpathSync(parent).startsWith(`${this.root}${path.sep}`)
    ) {
      throw new Error('unsafe path');
    }
    if (fs.lstatSync(target, {throwIfNoEntry: false})?.isSymbolicLink()) {
      throw new Error('unsafe path');
    }
    if (fs.existsSync(target) && !fs.realpathSync(target).startsWith(`${this.root}${path.sep}`)) {
      throw new Error('unsafe path');
    }
    return target;
  }
  upload(filename, data) {
    const target = this.safePath(path.basename(decodeURIComponent(filename)));
    fs.mkdirSync(path.dirname(target), {recursive: true});
    fs.writeFileSync(target, data);
    return {status: 'uploaded', path: `/${path.basename(target)}`};
  }

  exists(relative) {
    return fs.existsSync(this.safePath(relative));
  }

  list(relative) {
    const target = this.safePath(relative);
    if (!fs.existsSync(target) || !fs.statSync(target).isDirectory()) return null;
    return {path: `/${relative || ''}`, entries: fs.readdirSync(target).sort().slice(0, 4096)};
  }

  download(relative) {
    const target = this.safePath(relative);
    if (
      !fs.existsSync(target) ||
      fs.lstatSync(target).isSymbolicLink() ||
      !fs.statSync(target).isFile()
    )
      return null;
    const stat = fs.statSync(target);
    return {stream: fs.createReadStream(target), size: stat.size};
  }
}
