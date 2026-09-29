-- One database per simulated machine. Test credentials only; the stack binds
-- no database port to the host.
CREATE DATABASE source;
CREATE USER 'source'@'%' IDENTIFIED BY 'source';
GRANT ALL ON source.* TO 'source'@'%';

CREATE DATABASE target;
CREATE USER 'target'@'%' IDENTIFIED BY 'target';
GRANT ALL ON target.* TO 'target'@'%';

CREATE DATABASE workstation;
CREATE USER 'workstation'@'%' IDENTIFIED BY 'workstation';
GRANT ALL ON workstation.* TO 'workstation'@'%';
